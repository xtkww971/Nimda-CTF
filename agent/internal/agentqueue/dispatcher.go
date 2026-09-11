package agentqueue

import (
	"context"
	"log/slog"
	"os"

	protocol "nimda-ctf-protocol"
)

// 실제 도커 조작을 하는 쪽 Handler
type InstanceHandler interface {
	Create(ctx context.Context, req *protocol.RequestMessage) *protocol.ResponseMessage
	Destroy(ctx context.Context, uuid, instanceID string) *protocol.ResponseMessage
	// 이미 떠 있으면 그 접속 정보를, 아니면 nil을 돌려준다.
	Existing(ctx context.Context, uuid, instanceID string) *protocol.ResponseMessage
}

// uuid ↔ 컨테이너 기록.
type Registry interface {
	Save(ctx context.Context, uuid, instanceID string) error
	InstanceID(ctx context.Context, uuid string) (string, error)
	Delete(ctx context.Context, uuid string) error
}

// 요청 내용을 보고 무슨 작업인지 판단
// Provisioner TaskDispatcher와 같은 역할. 다른 점은 기준이 challengeCategory가 아니라
// Action이라는 것뿐이다 — 에이전트는 문제 유형을 알 필요가 없고, "CREATE/DESTROY"만 받는다.
type Dispatcher struct {
	instances InstanceHandler
	registry  Registry
	log       *slog.Logger
}

func NewDispatcher(instances InstanceHandler, registry Registry, log *slog.Logger) *Dispatcher {
	return &Dispatcher{instances: instances, registry: registry, log: log}
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
		return &protocol.ResponseMessage{
			UUID:    req.UUID,
			Action:  req.Action,
			Status:  protocol.StatusFailed,
			Message: "처리할 수 없는 action입니다: " + req.Action,
		}, nil
	}
}

func (d *Dispatcher) create(ctx context.Context, req *protocol.RequestMessage) (*protocol.ResponseMessage, error) {
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
			// todo SHARED인 경우와 PER_USER인 경우 작업 분리
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

func (d *Dispatcher) destroy(ctx context.Context, req *protocol.RequestMessage) (*protocol.ResponseMessage, error) {
	instanceID, err := d.registry.InstanceID(ctx, req.UUID)
	if err != nil {
		return nil, err
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
