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
	AllEntries(ctx context.Context) (map[string]*registry.Entry, error)
	Delete(ctx context.Context, uuid string) error
	ReleasePort(ctx context.Context, port int) error
}

type Reaper struct {
	rt       Containers
	store    Store
	log      *slog.Logger
	interval time.Duration
	now      func() time.Time // 테스트에서 바꾼다
}

func New(rt Containers, store Store, interval time.Duration, log *slog.Logger) *Reaper {
	return &Reaper{rt: rt, store: store, interval: interval, log: log, now: time.Now}
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
			continue // SHARED는 이 이슈 밖
		}
		if e.ExpiresAt.IsZero() || e.ExpiresAt.After(now) {
			continue // 만료되지 않음
		}
		r.reapPerUser(ctx, uuid, e)
	}

	r.reapOrphans(ctx, now, entries)
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
