// 모니터링 snapshot을 만들어 ctf-redis에 쓴다 (Feature-01).
//
// agent는 listener를 열지 않는다. 그래서 대시보드가 볼 정보를 주기적으로 Redis에 써둔다.
// Docker(실제 상태)와 registry(기록)를 맞춰보는 게 핵심이다.
// registry만 보면 컨테이너가 죽었는지, 기록 없이 떠 있는 컨테이너(orphan)가 있는지 알 수 없다.
package monitor

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"

	"nimda-ctf-agent/internal/registry"
	"nimda-ctf-agent/internal/runtime"
	protocol "nimda-ctf-protocol"
)

// snapshot 갱신 주기. TTL(30초)보다 충분히 짧아야 한 번 놓쳐도 키가 사라지지 않는다.
const Interval = 10 * time.Second

// 컨테이너 라벨 (agentserver.Create가 붙인다)
const (
	labelCode = "nimda.ctf.code"
	labelUUID = "nimda.ctf.uuid"
)

type ContainerLister interface {
	List(ctx context.Context) ([]runtime.ContainerInfo, error)
}

type RegistryReader interface {
	AllEntries(ctx context.Context) (map[string]*registry.Entry, error)
	AllShared(ctx context.Context) (map[string]*registry.SharedInstance, error)
	Leases(ctx context.Context, code string) ([]registry.Lease, error)
}

type Monitor struct {
	containers ContainerLister
	registry   RegistryReader
	rdb        *redis.Client
	log        *slog.Logger
	now        func() time.Time
}

func New(containers ContainerLister, reg RegistryReader, rdb *redis.Client, log *slog.Logger) *Monitor {
	return &Monitor{containers: containers, registry: reg, rdb: rdb, log: log, now: time.Now}
}

// ctx가 끝날 때까지 Interval마다 snapshot을 쓴다. 시작하자마자 한 번 쓴다.
func (m *Monitor) Run(ctx context.Context) {
	m.publish(ctx)
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.publish(ctx)
		}
	}
}

// 실패해도 다음 주기에 다시 한다. 계속 실패하면 TTL이 지나 키가 사라지고,
// 대시보드에 "agent 응답 없음"이 뜬다.
func (m *Monitor) publish(ctx context.Context) {
	snap, err := m.Build(ctx)
	if err != nil {
		m.log.WarnContext(ctx, "모니터링 snapshot 생성 실패", "error", err)
		return
	}
	b, err := json.Marshal(snap)
	if err != nil {
		m.log.WarnContext(ctx, "모니터링 snapshot 직렬화 실패", "error", err)
		return
	}
	if err := m.rdb.Set(ctx, protocol.MonitorSnapshotKey, b, protocol.MonitorSnapshotTTL).Err(); err != nil {
		m.log.WarnContext(ctx, "모니터링 snapshot 저장 실패", "error", err)
	}
}

// Docker와 registry를 읽어서 snapshot을 만든다.
func (m *Monitor) Build(ctx context.Context) (*protocol.MonitorSnapshot, error) {
	containers, err := m.containers.List(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := m.registry.AllEntries(ctx)
	if err != nil {
		return nil, err
	}
	shared, err := m.registry.AllShared(ctx)
	if err != nil {
		return nil, err
	}

	now := m.now().UTC()
	byID := make(map[string]runtime.ContainerInfo, len(containers))
	for _, c := range containers {
		byID[c.ID] = c
	}
	matched := map[string]bool{} // registry에 기록이 있는 컨테이너 ID

	snap := &protocol.MonitorSnapshot{
		GeneratedAt: now,
		Shared:      []protocol.MonitorShared{},
		PerUser:     []protocol.MonitorPerUser{},
		Orphans:     []protocol.MonitorOrphan{},
	}

	// SHARED: 문제마다 하나
	for code, s := range shared {
		leases, err := m.registry.Leases(ctx, code)
		if err != nil {
			return nil, err
		}
		item := protocol.MonitorShared{
			ChallengeCode: code,
			InstanceID:    shortID(s.InstanceID),
			Leases:        make([]protocol.MonitorLease, 0, len(leases)),
			CreatedAt:     timePtr(s.CreatedAt),
			IdleSince:     timePtr(s.IdleSince),
		}
		c, ok := byID[s.InstanceID]
		item.State, item.HostPort = state(c, ok)
		if ok {
			matched[c.ID] = true
			item.ContainerName = c.Name
		}
		for _, l := range leases {
			item.Leases = append(item.Leases, protocol.MonitorLease{
				UUID: l.UUID, ExpiresAt: l.ExpiresAt, Expired: !l.ExpiresAt.After(now),
			})
		}
		snap.Summary.Leases += len(leases)
		snap.Shared = append(snap.Shared, item)
	}

	// PER_USER: uuid마다 하나. SHARED의 uuid:는 lease 목록으로 이미 보여서 건너뛴다.
	for uuid, e := range entries {
		if e.Isolation != protocol.IsolationPerUser {
			continue
		}
		item := protocol.MonitorPerUser{UUID: uuid, InstanceID: shortID(e.InstanceID)}
		c, ok := byID[e.InstanceID]
		item.State, item.HostPort = state(c, ok)
		if ok {
			matched[c.ID] = true
			item.ContainerName = c.Name
			item.ChallengeCode = c.Labels[labelCode]
			item.CreatedAt = timePtr(c.Created)
		}
		snap.PerUser = append(snap.PerUser, item)
	}

	// orphan: 라벨은 있는데 registry 어디에도 없는 컨테이너
	for _, c := range containers {
		if c.Running {
			snap.Summary.Running++
		}
		if matched[c.ID] {
			continue
		}
		st, _ := state(c, true)
		snap.Orphans = append(snap.Orphans, protocol.MonitorOrphan{
			InstanceID:    shortID(c.ID),
			ContainerName: c.Name,
			ChallengeCode: c.Labels[labelCode],
			UUID:          c.Labels[labelUUID],
			State:         st,
			CreatedAt:     timePtr(c.Created),
		})
	}

	// 화면에서 순서가 매번 바뀌지 않게 정렬한다. map 순회 순서는 매번 다르다.
	sort.Slice(snap.Shared, func(i, j int) bool { return snap.Shared[i].ChallengeCode < snap.Shared[j].ChallengeCode })
	sort.Slice(snap.PerUser, func(i, j int) bool { return snap.PerUser[i].UUID < snap.PerUser[j].UUID })
	sort.Slice(snap.Orphans, func(i, j int) bool { return snap.Orphans[i].ContainerName < snap.Orphans[j].ContainerName })

	snap.Summary.Containers = len(containers)
	snap.Summary.Shared = len(snap.Shared)
	snap.Summary.PerUser = len(snap.PerUser)
	snap.Summary.Orphans = len(snap.Orphans)
	return snap, nil
}

// 컨테이너 상태와 host 포트. 포트는 떠 있을 때만 준다.
func state(c runtime.ContainerInfo, exists bool) (string, *int) {
	if !exists {
		return protocol.MonitorMissing, nil
	}
	if !c.Running {
		return protocol.MonitorExited, nil
	}
	if c.HostPort == 0 {
		return protocol.MonitorRunning, nil
	}
	p := c.HostPort
	return protocol.MonitorRunning, &p
}

// docker ps와 같은 길이로 자른다.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
