package agentqueue

import (
	"context"
	"log/slog"
	"os"
	"time"

	"nimda-ctf-agent/internal/registry"
	protocol "nimda-ctf-protocol"
)

// 실제 도커 조작을 하는 쪽 Handler
type InstanceHandler interface {
	Create(ctx context.Context, req *protocol.RequestMessage) *protocol.ResponseMessage
	Destroy(ctx context.Context, uuid, instanceID string) *protocol.ResponseMessage
	// 이미 떠 있으면 그 접속 정보를, 아니면 nil을 돌려준다.
	Existing(ctx context.Context, uuid, instanceID string) *protocol.ResponseMessage
}

// uuid ↔ 컨테이너 기록. 구현은 registry 패키지(ADR-0019).
type Registry interface {
	// PER_USER
	Save(ctx context.Context, uuid, instanceID string) error
	InstanceID(ctx context.Context, uuid string) (string, error)
	Delete(ctx context.Context, uuid string) error

	// uuid: 전체. DESTROY는 이걸로 PER_USER인지 SHARED인지 판단한다.
	Entry(ctx context.Context, uuid string) (*registry.Entry, error)

	// SHARED
	Shared(ctx context.Context, code string) (*registry.SharedInstance, error)
	SaveShared(ctx context.Context, code, instanceID string) error
	Join(ctx context.Context, code, uuid string, expiresAt time.Time) (time.Time, error)
	Leave(ctx context.Context, code, uuid string) error
	Lock(ctx context.Context, code string, ttl time.Duration) (token string, ok bool, err error)
	Unlock(ctx context.Context, code, token string) error
}

const (
	// SHARED lease 길이. 요청 메시지에 TTL 필드가 생기면 그 값을 쓴다(ADR-0014).
	sharedLeaseTTL = time.Hour

	// lock TTL. 빌드가 이보다 오래 걸리면 lock이 먼저 풀린다.
	// todo 빌드 timeout을 따로 정하고 그보다 길게 맞춘다(ADR-0019).
	sharedLockTTL = 10 * time.Minute

	// lock을 못 잡았을 때 다시 시도하는 간격과 최대 대기 시간.
	// agent consumer에 XAUTOCLAIM이 없어서 에러를 반환해 redelivery를 기다릴 수 없다.
	// 그래서 잠깐 기다려 보고, 그래도 안 되면 FAILED를 돌려준다.
	lockRetryInterval = time.Second
	lockWaitTimeout   = 30 * time.Second
)

// 요청 내용을 보고 무슨 작업인지 판단
// Provisioner TaskDispatcher와 같은 역할. 다른 점은 기준이 challengeCategory가 아니라
// Action이라는 것뿐이다 — 에이전트는 문제 유형을 알 필요가 없고, "CREATE/DESTROY"만 받는다.
type Dispatcher struct {
	instances InstanceHandler
	registry  Registry
	log       *slog.Logger

	// 테스트에서 줄이려고 필드로 둔다.
	lockRetryInterval time.Duration
	lockWaitTimeout   time.Duration
	now               func() time.Time
}

func NewDispatcher(instances InstanceHandler, registry Registry, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		instances:         instances,
		registry:          registry,
		log:               log,
		lockRetryInterval: lockRetryInterval,
		lockWaitTimeout:   lockWaitTimeout,
		now:               time.Now,
	}
}

var _ Handler = (*Dispatcher)(nil)

func (d *Dispatcher) Handle(ctx context.Context, req *protocol.RequestMessage) (*protocol.ResponseMessage, error) {
	switch req.Action {
	case protocol.ActionCreate:
		return d.create(ctx, req)

	case protocol.ActionDestroy:
		return d.destroy(ctx, req)

	default:
		// 파서가 모르는 action을 걸러내므로 여기까지 오지 않는다. 그래도 두는 이유는
		// action을 하나 추가하면서 여기 case를 잊는 경우가 컴파일러에 안 걸리기 때문이다.
		d.log.ErrorContext(ctx, "처리할 수 없는 action", "uuid", req.UUID, "action", req.Action)
		return failed(req.UUID, req.Action, "처리할 수 없는 action입니다: "+req.Action), nil
	}
}

// PER_USER인지 SHARED인지에 따라 나눈다.
func (d *Dispatcher) create(ctx context.Context, req *protocol.RequestMessage) (*protocol.ResponseMessage, error) {
	switch req.IsolationType {
	case protocol.IsolationPerUser:
		return d.createPerUser(ctx, req)

	case protocol.IsolationShared:
		return d.createShared(ctx, req)

	default:
		// 파서가 막지만, 값이 새로 추가됐을 때 덜 격리된 쪽으로 가지 않게 여기서 한 번 더 막는다.
		d.cleanupBuildDir(ctx, req)
		d.log.ErrorContext(ctx, "알 수 없는 isolationType", "uuid", req.UUID, "isolation", req.IsolationType)
		return failed(req.UUID, protocol.ActionCreate, "알 수 없는 isolationType입니다: "+string(req.IsolationType)), nil
	}
}

// 요청마다 컨테이너를 하나 만든다.
func (d *Dispatcher) createPerUser(ctx context.Context, req *protocol.RequestMessage) (*protocol.ResponseMessage, error) {
	// Redis Stream은 at-least-once다 — 처리 중에 죽거나 ACK이 늦으면 같은 요청이 다시
	// 배달된다. 그대로 다시 만들면 runtime.Run이 같은 이름의 컨테이너를 docker rm -f로
	// 지우고 새로 띄우므로, 참가자가 풀고 있던 인스턴스가 초기화된다.
	instanceID, err := d.registry.InstanceID(ctx, req.UUID)
	if err != nil {
		// 컨테이너 id를 읽지 못한 경우
		return nil, err
	}
	if instanceID != "" {
		if res := d.instances.Existing(ctx, req.UUID, instanceID); res != nil {
			// 인스턴스가 존재하는 경우
			d.cleanupBuildDir(ctx, req)
			return res, nil
		}
		// redis에는 인스턴스가 있지만 실제로 없는 경우
		// todo TTL이 지나서 없어지는 경우에 인스턴스도 같이 정보 동기화를 해야 함
		d.log.WarnContext(ctx, "기록된 인스턴스가 살아있지 않음, 다시 만듭니다",
			"uuid", req.UUID, "instanceId", instanceID)
	}

	res := d.instances.Create(ctx, req)

	// 빌드가 끝난 이후 폴더 삭제
	d.cleanupBuildDir(ctx, req)

	if res.Status != protocol.StatusReady {
		return res, nil
	}

	// 기록을 남긴다. 실패해도 인스턴스는 이미 떠 있으므로 응답은 그대로 보낸다 —
	// 대신 다음 재배달 때 재사용을 못 하고 다시 만들게 된다.
	if err := d.registry.Save(ctx, req.UUID, res.InstanceID); err != nil {
		d.log.ErrorContext(ctx, "인스턴스 기록 실패, 재배달 시 중복 생성될 수 있음",
			"uuid", req.UUID, "instanceId", res.InstanceID, "error", err)
	}
	return res, nil
}

// 문제당 컨테이너 하나를 같이 쓴다(ADR-0019).
//
//  1. lock을 잡는다
//  2. shared:{code}가 running이면 빌드 없이 연결, 아니면 빌드하고 저장
//  3. lease를 추가하고 uuid: 를 저장
//  4. lock을 풀고 이 참가자의 lease 만료 시각을 응답에 넣는다
func (d *Dispatcher) createShared(ctx context.Context, req *protocol.RequestMessage) (*protocol.ResponseMessage, error) {
	code := req.ChallengeCode

	// 어느 경로로 끝나든 빌드 폴더는 치운다. 연결만 하는 경우에도 조율자는 압축을 풀어서 넘긴다.
	defer d.cleanupBuildDir(ctx, req)

	token, ok, err := d.waitLock(ctx, code)
	if err != nil {
		d.log.ErrorContext(ctx, "lock 획득 실패", "uuid", req.UUID, "code", code, "error", err)
		return failed(req.UUID, protocol.ActionCreate, "공유 인스턴스 lock을 잡지 못했습니다"), nil
	}
	if !ok {
		d.log.WarnContext(ctx, "lock 대기 시간 초과", "uuid", req.UUID, "code", code)
		return failed(req.UUID, protocol.ActionCreate, "같은 문제의 인스턴스를 준비 중입니다. 잠시 후 다시 시도해 주세요"), nil
	}
	// 요청 ctx가 취소돼도 lock은 풀어야 한다. 안 풀면 TTL 동안 이 문제는 아무도 못 연다.
	defer func() {
		if err := d.registry.Unlock(context.WithoutCancel(ctx), code, token); err != nil {
			d.log.ErrorContext(ctx, "lock 해제 실패", "code", code, "error", err)
		}
	}()

	res, err := d.sharedInstance(ctx, req)
	if err != nil {
		d.log.ErrorContext(ctx, "공유 인스턴스 기록 실패", "uuid", req.UUID, "code", code, "error", err)
		return failed(req.UUID, protocol.ActionCreate, "공유 인스턴스 기록에 실패했습니다"), nil
	}
	if res.Status != protocol.StatusReady {
		return res, nil
	}

	expiresAt, err := d.registry.Join(ctx, code, req.UUID, d.now().Add(sharedLeaseTTL))
	if err != nil {
		// lease가 없으면 나중에 reaper가 사용 중인 컨테이너를 지울 수 있다. 접속 정보를 주지 않는다.
		d.log.ErrorContext(ctx, "lease 기록 실패", "uuid", req.UUID, "code", code, "error", err)
		return failed(req.UUID, protocol.ActionCreate, "공유 인스턴스 참가 기록에 실패했습니다"), nil
	}

	// handler는 SHARED에 만료 시각을 넣지 않는다. 이 참가자의 lease 만료 시각을 넣는다.
	res.UUID = req.UUID
	res.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	d.log.InfoContext(ctx, "공유 인스턴스 참가", "uuid", req.UUID, "code", code,
		"instanceId", res.InstanceID, "expiresAt", res.ExpiresAt)
	return res, nil
}

// 떠 있는 공유 컨테이너를 찾고, 없으면 만든다. lock을 잡은 상태에서 부른다.
func (d *Dispatcher) sharedInstance(ctx context.Context, req *protocol.RequestMessage) (*protocol.ResponseMessage, error) {
	code := req.ChallengeCode

	shared, err := d.registry.Shared(ctx, code)
	if err != nil {
		return nil, err
	}
	if shared != nil {
		if res := d.instances.Existing(ctx, req.UUID, shared.InstanceID); res != nil {
			return res, nil
		}
		d.log.WarnContext(ctx, "기록된 공유 인스턴스가 살아있지 않음, 다시 만듭니다",
			"code", code, "instanceId", shared.InstanceID)
	}

	res := d.instances.Create(ctx, req)
	if res.Status != protocol.StatusReady {
		return res, nil
	}
	// 기록을 못 남기면 다음 참가자가 또 빌드하면서 이 컨테이너를 지운다. 실패로 처리한다.
	if err := d.registry.SaveShared(ctx, code, res.InstanceID); err != nil {
		return nil, err
	}
	return res, nil
}

// lock을 잡을 때까지 lockRetryInterval 간격으로 다시 시도한다.
// lockWaitTimeout이 지나면 ok=false를 돌려준다.
func (d *Dispatcher) waitLock(ctx context.Context, code string) (token string, ok bool, err error) {
	deadline := d.now().Add(d.lockWaitTimeout)
	for {
		token, ok, err = d.registry.Lock(ctx, code, sharedLockTTL)
		if err != nil || ok {
			return token, ok, err
		}
		if !d.now().Before(deadline) {
			return "", false, nil
		}
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-time.After(d.lockRetryInterval):
		}
	}
}

// DESTROY 메시지에는 uuid만 있다. uuid: 를 읽어서 PER_USER인지 SHARED인지 판단한다.
func (d *Dispatcher) destroy(ctx context.Context, req *protocol.RequestMessage) (*protocol.ResponseMessage, error) {
	entry, err := d.registry.Entry(ctx, req.UUID)
	if err != nil {
		return nil, err
	}

	if entry != nil && entry.Isolation == protocol.IsolationShared {
		// 자기 lease만 반납한다. 다른 참가자가 쓰고 있을 수 있어서 컨테이너는 건드리지 않는다.
		if err := d.registry.Leave(ctx, entry.ChallengeCode, req.UUID); err != nil {
			return nil, err
		}
		return &protocol.ResponseMessage{UUID: req.UUID, Action: protocol.ActionDestroy, Status: protocol.StatusDestroyed}, nil
	}

	// PER_USER, 또는 기록이 없는 경우(instanceID가 비어 있으면 handler가 NOT_FOUND를 돌려준다)
	instanceID := ""
	if entry != nil {
		instanceID = entry.InstanceID
	}
	res := d.instances.Destroy(ctx, req.UUID, instanceID)

	// 인스턴스 파괴 시 redis 정보 동기화
	if res.Status == protocol.StatusDestroyed {
		if err := d.registry.Delete(ctx, req.UUID); err != nil {
			d.log.ErrorContext(ctx, "인스턴스 기록 삭제 실패", "uuid", req.UUID, "error", err)
		}
	}
	return res, nil
}

// 빌드 폴더 삭제 함수
func (d *Dispatcher) cleanupBuildDir(ctx context.Context, req *protocol.RequestMessage) {
	if req.BuildContextPath == "" {
		return
	}
	if err := os.RemoveAll(req.BuildContextPath); err != nil {
		// 못 지워도 인스턴스는 떴다. 볼륨이 조금 차는 것뿐이라 실패로 보지 않는다.
		d.log.WarnContext(ctx, "빌드 폴더 정리 실패", "uuid", req.UUID,
			"path", req.BuildContextPath, "error", err)
	}
}

func failed(uuid, action, msg string) *protocol.ResponseMessage {
	return &protocol.ResponseMessage{
		UUID:    uuid,
		Action:  action,
		Status:  protocol.StatusFailed,
		Message: msg,
	}
}
