// reaper는 만료된 PER_USER 인스턴스를 주기적으로 훑어 내린다(ADR-0014).
// "만료는 선언이 아니라 집행이다" — 시각을 계산해 보내는 것만으로는 아무것도 회수되지 않는다.
//
// 컨테이너를 만질 수 있는 건 agent뿐이라 여기 둔다(ADR-0009).
// SHARED의 idle grace 회수는 이 이슈 밖이다. Isolation==PER_USER만 본다.
package reaper

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"nimda-ctf-agent/internal/registry"
	"nimda-ctf-agent/internal/runtime"
	protocol "nimda-ctf-protocol"
)

// 컨테이너를 보고 지우는 쪽. 구현은 runtime.Docker.
type Containers interface {
	Remove(ctx context.Context, nameOrID string) error
	List(ctx context.Context) ([]runtime.ContainerInfo, error)
}

// registry 조회·정리. 구현은 registry.Registry.
type Store interface {
	// PER_USER
	AllEntries(ctx context.Context) (map[string]*registry.Entry, error)
	Delete(ctx context.Context, uuid string) error
	ReleasePort(ctx context.Context, port int) error

	// SHARED (ADR-0019)
	AllShared(ctx context.Context) (map[string]*registry.SharedInstance, error)
	PruneExpiredLeases(ctx context.Context, code string, now time.Time) ([]string, error)
	MarkIdle(ctx context.Context, code string, now time.Time) error
	LeaseCount(ctx context.Context, code string) (int64, error)
	Lock(ctx context.Context, code string, ttl time.Duration) (token string, ok bool, err error)
	Unlock(ctx context.Context, code, token string) error
	DeleteShared(ctx context.Context, code string) error
}

// SHARED 컨테이너를 내리는 동안 잡는 lock의 수명. 컨테이너 삭제보다 길어야 한다.
// 이 시간을 넘겨 lock이 먼저 풀리면, 지우는 사이에 CREATE가 끼어 race가 난다(ADR-0019).
const sharedReapLockTTL = 30 * time.Second

type Reaper struct {
	rt       Containers
	store    Store
	log      *slog.Logger
	interval time.Duration
	grace    time.Duration // SHARED가 lease 0개가 된 뒤 이만큼 더 두고 내린다
	now      func() time.Time // 테스트에서 바꾼다
}

func New(rt Containers, store Store, interval, grace time.Duration, log *slog.Logger) *Reaper {
	return &Reaper{rt: rt, store: store, interval: interval, grace: grace, log: log, now: time.Now}
}

// 주기적으로 reap을 돈다. ctx가 끝나면 멈춘다.
func (r *Reaper) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	r.log.Info("reaper 시작", "interval", r.interval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.reap(ctx)
		}
	}
}

// 한 바퀴. registry 경로(정상)와 도커 경로(orphan 2차 안전장치)를 둘 다 본다.
func (r *Reaper) reap(ctx context.Context) {
	now := r.now().UTC()

	entries, err := r.store.AllEntries(ctx)
	if err != nil {
		// registry를 못 읽어도 orphan 경로는 볼 수 있으니 멈추지 않는다.
		r.log.ErrorContext(ctx, "reaper: registry 조회 실패", "error", err)
	}
	for uuid, e := range entries {
		if e.Isolation != protocol.IsolationPerUser {
			continue // SHARED는 아래 reapAllShared가 본다
		}
		if e.ExpiresAt.IsZero() || e.ExpiresAt.After(now) {
			continue // 만료되지 않음
		}
		r.reapPerUser(ctx, uuid, e)
	}

	r.reapAllShared(ctx, now)
	r.reapOrphans(ctx, now, entries)
}

// 공유 컨테이너를 문제별로 본다(ADR-0019 ⑤).
func (r *Reaper) reapAllShared(ctx context.Context, now time.Time) {
	shared, err := r.store.AllShared(ctx)
	if err != nil {
		r.log.ErrorContext(ctx, "reaper: 공유 인스턴스 조회 실패", "error", err)
		return
	}
	for code, s := range shared {
		r.reapShared(ctx, now, code, s)
	}
}

func (r *Reaper) reapShared(ctx context.Context, now time.Time, code string, s *registry.SharedInstance) {
	// ① 만료된 lease를 정리한다. lock 없이 해도 된다(이미 만료된 참가 기록이라 CREATE와 안 겹친다).
	if pruned, err := r.store.PruneExpiredLeases(ctx, code, now); err != nil {
		r.log.ErrorContext(ctx, "reaper: 만료 lease 정리 실패", "code", code, "error", err)
	} else if len(pruned) > 0 {
		r.log.InfoContext(ctx, "reaper: 만료 lease 정리", "code", code, "count", len(pruned))
	}

	// ② 컨테이너를 내릴지 판단한다. CREATE와 같은 lock을 잡아야 "지우는 찰나 새 참가자가 붙는" race를 막는다.
	token, ok, err := r.store.Lock(ctx, code, sharedReapLockTTL)
	if err != nil {
		r.log.ErrorContext(ctx, "reaper: lock 획득 실패", "code", code, "error", err)
		return
	}
	if !ok {
		return // 지금 CREATE가 잡고 있다. 다음 주기에 다시 본다.
	}
	defer func() {
		if err := r.store.Unlock(context.WithoutCancel(ctx), code, token); err != nil {
			r.log.ErrorContext(ctx, "reaper: lock 해제 실패", "code", code, "error", err)
		}
	}()

	n, err := r.store.LeaseCount(ctx, code)
	if err != nil {
		r.log.ErrorContext(ctx, "reaper: lease 개수 조회 실패", "code", code, "error", err)
		return
	}
	if n > 0 {
		return // 아직 쓰는 사람이 있다
	}

	if s.IdleSince.IsZero() {
		// 방금 0개가 됐다. grace를 재기 시작할 시각만 찍고 이번엔 안 내린다.
		if err := r.store.MarkIdle(ctx, code, now); err != nil {
			r.log.ErrorContext(ctx, "reaper: idle 시각 기록 실패", "code", code, "error", err)
		}
		return
	}
	if now.Sub(s.IdleSince) < r.grace {
		return // grace가 아직 안 지났다
	}

	// lease 0개 + grace 경과 → 컨테이너와 기록을 내린다.
	if s.InstanceID != "" {
		if err := r.rt.Remove(ctx, s.InstanceID); err != nil {
			r.log.ErrorContext(ctx, "reaper: 공유 컨테이너 삭제 실패, 다음 주기에 재시도",
				"code", code, "instanceId", s.InstanceID, "error", err)
			return
		}
	}
	if err := r.store.DeleteShared(ctx, code); err != nil {
		r.log.ErrorContext(ctx, "reaper: 공유 기록 삭제 실패", "code", code, "error", err)
	}
	r.log.InfoContext(ctx, "reaper: idle 공유 인스턴스 회수", "code", code)
}

// registry에 기록이 있는 만료 인스턴스를 내린다.
func (r *Reaper) reapPerUser(ctx context.Context, uuid string, e *registry.Entry) {
	if e.InstanceID != "" {
		if err := r.rt.Remove(ctx, e.InstanceID); err != nil {
			// 컨테이너가 안 지워졌다. 기록·포트를 건드리지 않고 다음 주기에 다시 시도한다.
			r.log.ErrorContext(ctx, "reaper: 컨테이너 삭제 실패, 다음 주기에 재시도",
				"uuid", uuid, "instanceId", e.InstanceID, "error", err)
			return
		}
	}
	if err := r.store.Delete(ctx, uuid); err != nil {
		r.log.ErrorContext(ctx, "reaper: 기록 삭제 실패", "uuid", uuid, "error", err)
	}
	if e.Port != 0 {
		if err := r.store.ReleasePort(ctx, e.Port); err != nil {
			r.log.ErrorContext(ctx, "reaper: 포트 반납 실패", "uuid", uuid, "port", e.Port, "error", err)
		}
	}
	r.log.InfoContext(ctx, "reaper: 만료 인스턴스 회수", "uuid", uuid, "port", e.Port)
}

// registry 기록을 잃은 컨테이너를 라벨의 만료 시각으로 회수한다(ADR-0014의 2차 안전장치).
// 기록을 쓰기 전에 죽거나, 기록을 지운 뒤 삭제에 실패한 경우가 여기 걸린다.
func (r *Reaper) reapOrphans(ctx context.Context, now time.Time, entries map[string]*registry.Entry) {
	list, err := r.rt.List(ctx)
	if err != nil {
		r.log.ErrorContext(ctx, "reaper: 컨테이너 목록 조회 실패", "error", err)
		return
	}
	for _, c := range list {
		raw, ok := c.Labels[runtime.LabelExpires]
		if !ok {
			continue // 만료 라벨이 없다(SHARED 등). orphan 회수 대상이 아니다.
		}
		// registry가 아는 uuid면 위의 정상 경로가 처리한다.
		if uuid := c.Labels[runtime.LabelUUID]; uuid != "" && entries[uuid] != nil {
			continue
		}
		exp, err := time.Parse(time.RFC3339, raw)
		if err != nil || exp.After(now) {
			continue // 해석 못 하거나 아직 안 만료
		}
		if err := r.rt.Remove(ctx, c.ID); err != nil {
			r.log.ErrorContext(ctx, "reaper: orphan 삭제 실패", "id", c.ID, "error", err)
			continue
		}
		// orphan이 TCP 포트를 쥐고 있었으면 풀에 돌려준다. 기록이 없어 라벨로만 알 수 있다.
		if p, err := strconv.Atoi(c.Labels[runtime.LabelPort]); err == nil && p != 0 {
			if err := r.store.ReleasePort(ctx, p); err != nil {
				r.log.ErrorContext(ctx, "reaper: orphan 포트 반납 실패", "port", p, "error", err)
			}
		}
		r.log.InfoContext(ctx, "reaper: orphan 회수", "id", c.ID, "uuid", c.Labels[runtime.LabelUUID])
	}
}
