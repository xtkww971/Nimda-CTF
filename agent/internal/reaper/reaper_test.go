package reaper

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"nimda-ctf-agent/internal/registry"
	"nimda-ctf-agent/internal/runtime"
	protocol "nimda-ctf-protocol"
)

// 도커 대신 "무엇을 지웠는지"만 기록하는 fake.
type fakeContainers struct {
	list      []runtime.ContainerInfo
	removed   []string
	removeErr map[string]error // id → 반환할 에러
}

func (f *fakeContainers) Remove(_ context.Context, id string) error {
	if err := f.removeErr[id]; err != nil {
		return err
	}
	f.removed = append(f.removed, id)
	return nil
}
func (f *fakeContainers) List(context.Context) ([]runtime.ContainerInfo, error) {
	return f.list, nil
}

type fakeStore struct {
	entries  map[string]*registry.Entry
	deleted  []string
	released []int

	// SHARED
	shared       map[string]*registry.SharedInstance
	leaseCount   map[string]int64
	locked       map[string]bool // 이미 누가 잡고 있는 code (Lock이 false 반환)
	pruned       map[string]bool // PruneExpiredLeases 호출된 code
	markedIdle   []string        // MarkIdle 호출된 code
	deletedShare []string        // DeleteShared 호출된 code
}

func (s *fakeStore) AllEntries(context.Context) (map[string]*registry.Entry, error) {
	return s.entries, nil
}
func (s *fakeStore) Delete(_ context.Context, uuid string) error {
	s.deleted = append(s.deleted, uuid)
	delete(s.entries, uuid)
	return nil
}
func (s *fakeStore) ReleasePort(_ context.Context, port int) error {
	s.released = append(s.released, port)
	return nil
}
func (s *fakeStore) AllShared(context.Context) (map[string]*registry.SharedInstance, error) {
	return s.shared, nil
}
func (s *fakeStore) PruneExpiredLeases(_ context.Context, code string, _ time.Time) ([]string, error) {
	if s.pruned == nil {
		s.pruned = map[string]bool{}
	}
	s.pruned[code] = true
	return nil, nil
}
func (s *fakeStore) MarkIdle(_ context.Context, code string, _ time.Time) error {
	s.markedIdle = append(s.markedIdle, code)
	return nil
}
func (s *fakeStore) LeaseCount(_ context.Context, code string) (int64, error) {
	return s.leaseCount[code], nil
}
func (s *fakeStore) Lock(_ context.Context, code string, _ time.Duration) (string, bool, error) {
	if s.locked[code] {
		return "", false, nil
	}
	return "tok", true, nil
}
func (s *fakeStore) Unlock(context.Context, string, string) error { return nil }
func (s *fakeStore) DeleteShared(_ context.Context, code string) error {
	s.deletedShare = append(s.deletedShare, code)
	return nil
}

func newReaper(rt Containers, store Store) *Reaper {
	r := New(rt, store, time.Minute, 10*time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.now = func() time.Time { return time.Unix(1_000_000, 0).UTC() } // 고정 시각
	return r
}

func past() time.Time   { return time.Unix(999_000, 0).UTC() }   // now보다 전
func future() time.Time { return time.Unix(1_001_000, 0).UTC() } // now보다 후

// 만료된 PER_USER는 컨테이너 삭제 + 기록 삭제 + 포트 반납.
func TestReapsExpiredPerUser(t *testing.T) {
	rt := &fakeContainers{}
	store := &fakeStore{entries: map[string]*registry.Entry{
		"u-1": {Isolation: protocol.IsolationPerUser, InstanceID: "c-1", Port: 30042, ExpiresAt: past()},
	}}
	newReaper(rt, store).reap(context.Background())

	if len(rt.removed) != 1 || rt.removed[0] != "c-1" {
		t.Errorf("컨테이너를 안 지움: %v", rt.removed)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "u-1" {
		t.Errorf("기록을 안 지움: %v", store.deleted)
	}
	if len(store.released) != 1 || store.released[0] != 30042 {
		t.Errorf("포트를 안 돌려줌: %v", store.released)
	}
}

// 아직 안 만료된 것은 건드리지 않는다.
func TestKeepsUnexpired(t *testing.T) {
	rt := &fakeContainers{}
	store := &fakeStore{entries: map[string]*registry.Entry{
		"u-1": {Isolation: protocol.IsolationPerUser, InstanceID: "c-1", ExpiresAt: future()},
	}}
	newReaper(rt, store).reap(context.Background())

	if len(rt.removed) != 0 || len(store.deleted) != 0 {
		t.Errorf("안 만료된 걸 지움: removed=%v deleted=%v", rt.removed, store.deleted)
	}
}

// SHARED는 이 reaper가 건드리지 않는다. 만료됐어도.
func TestIgnoresShared(t *testing.T) {
	rt := &fakeContainers{}
	store := &fakeStore{entries: map[string]*registry.Entry{
		"u-1": {Isolation: protocol.IsolationShared, InstanceID: "c-1", ExpiresAt: past()},
	}}
	newReaper(rt, store).reap(context.Background())

	if len(rt.removed) != 0 {
		t.Errorf("SHARED를 지움: %v", rt.removed)
	}
}

// expiresAt이 없는(zero) 기록은 건드리지 않는다.
func TestKeepsZeroExpiry(t *testing.T) {
	rt := &fakeContainers{}
	store := &fakeStore{entries: map[string]*registry.Entry{
		"u-1": {Isolation: protocol.IsolationPerUser, InstanceID: "c-1"},
	}}
	newReaper(rt, store).reap(context.Background())
	if len(rt.removed) != 0 {
		t.Errorf("만료 시각 없는 걸 지움: %v", rt.removed)
	}
}

// 컨테이너 삭제가 실패하면 기록·포트를 건드리지 않고 다음 주기에 다시 시도한다.
func TestRetriesOnRemoveFailure(t *testing.T) {
	rt := &fakeContainers{removeErr: map[string]error{"c-1": errors.New("docker 응답 없음")}}
	store := &fakeStore{entries: map[string]*registry.Entry{
		"u-1": {Isolation: protocol.IsolationPerUser, InstanceID: "c-1", Port: 30042, ExpiresAt: past()},
	}}
	newReaper(rt, store).reap(context.Background())

	if len(store.deleted) != 0 {
		t.Errorf("삭제 실패인데 기록을 지움: %v", store.deleted)
	}
	if len(store.released) != 0 {
		t.Errorf("삭제 실패인데 포트를 돌려줌: %v", store.released)
	}
}

// registry 기록이 없는데 만료 라벨이 지난 컨테이너는 orphan으로 회수한다.
func TestReapsOrphan(t *testing.T) {
	rt := &fakeContainers{list: []runtime.ContainerInfo{
		{ID: "orphan-1", Labels: map[string]string{
			runtime.LabelUUID:    "u-gone",
			runtime.LabelExpires: past().Format(time.RFC3339),
			runtime.LabelPort:    "30050",
		}},
	}}
	store := &fakeStore{entries: map[string]*registry.Entry{}} // 기록 없음
	newReaper(rt, store).reap(context.Background())

	if len(rt.removed) != 1 || rt.removed[0] != "orphan-1" {
		t.Errorf("orphan을 안 지움: %v", rt.removed)
	}
	if len(store.released) != 1 || store.released[0] != 30050 {
		t.Errorf("orphan 포트를 안 돌려줌: %v", store.released)
	}
}

// registry가 아는 컨테이너는 orphan 경로가 중복 처리하지 않는다.
func TestOrphanSkipsKnown(t *testing.T) {
	rt := &fakeContainers{list: []runtime.ContainerInfo{
		{ID: "c-1", Labels: map[string]string{
			runtime.LabelUUID:    "u-1",
			runtime.LabelExpires: past().Format(time.RFC3339),
		}},
	}}
	// 같은 uuid가 registry에 있고, 아직 안 만료 → 정상 경로가 유지하고 orphan도 건드리면 안 됨
	store := &fakeStore{entries: map[string]*registry.Entry{
		"u-1": {Isolation: protocol.IsolationPerUser, InstanceID: "c-1", ExpiresAt: future()},
	}}
	newReaper(rt, store).reap(context.Background())

	if len(rt.removed) != 0 {
		t.Errorf("registry가 아는 컨테이너를 orphan으로 지움: %v", rt.removed)
	}
}

// ── SHARED idle 회수 (#34) ──────────────────────────────────────────────────

// 만료 lease 정리는 lock과 무관하게 매번 돈다.
func TestSharedPrunesLeasesAlways(t *testing.T) {
	rt := &fakeContainers{}
	store := &fakeStore{
		shared:     map[string]*registry.SharedInstance{"web-01": {InstanceID: "c-1"}},
		leaseCount: map[string]int64{"web-01": 3}, // 쓰는 사람 있음
	}
	newReaper(rt, store).reap(context.Background())

	if !store.pruned["web-01"] {
		t.Error("만료 lease 정리를 호출하지 않음")
	}
	if len(rt.removed) != 0 || len(store.deletedShare) != 0 {
		t.Errorf("lease가 남았는데 컨테이너를 내림: removed=%v deleted=%v", rt.removed, store.deletedShare)
	}
}

// lease가 0개가 된 순간엔 바로 안 내리고 idleSince만 찍는다.
func TestSharedMarksIdleFirst(t *testing.T) {
	rt := &fakeContainers{}
	store := &fakeStore{
		shared:     map[string]*registry.SharedInstance{"web-01": {InstanceID: "c-1"}}, // IdleSince zero
		leaseCount: map[string]int64{"web-01": 0},
	}
	newReaper(rt, store).reap(context.Background())

	if len(store.markedIdle) != 1 || store.markedIdle[0] != "web-01" {
		t.Errorf("idleSince를 안 찍음: %v", store.markedIdle)
	}
	if len(rt.removed) != 0 {
		t.Errorf("idle 찍은 첫 주기에 내림: %v", rt.removed)
	}
}

// idleSince로부터 grace가 안 지났으면 유지한다.
func TestSharedKeepsWithinGrace(t *testing.T) {
	rt := &fakeContainers{}
	store := &fakeStore{
		shared: map[string]*registry.SharedInstance{
			"web-01": {InstanceID: "c-1", IdleSince: time.Unix(1_000_000-300, 0).UTC()}, // now-5m < 10m
		},
		leaseCount: map[string]int64{"web-01": 0},
	}
	newReaper(rt, store).reap(context.Background())

	if len(rt.removed) != 0 || len(store.deletedShare) != 0 {
		t.Errorf("grace 안 지났는데 내림: removed=%v deleted=%v", rt.removed, store.deletedShare)
	}
}

// lease 0개 + grace 경과 → 컨테이너와 기록을 내린다.
func TestSharedReapsAfterGrace(t *testing.T) {
	rt := &fakeContainers{}
	store := &fakeStore{
		shared: map[string]*registry.SharedInstance{
			"web-01": {InstanceID: "c-1", IdleSince: time.Unix(1_000_000-1200, 0).UTC()}, // now-20m > 10m
		},
		leaseCount: map[string]int64{"web-01": 0},
	}
	newReaper(rt, store).reap(context.Background())

	if len(rt.removed) != 1 || rt.removed[0] != "c-1" {
		t.Errorf("grace 지났는데 컨테이너를 안 내림: %v", rt.removed)
	}
	if len(store.deletedShare) != 1 || store.deletedShare[0] != "web-01" {
		t.Errorf("공유 기록을 안 지움: %v", store.deletedShare)
	}
}

// CREATE가 lock을 잡고 있으면(지우는 찰나 새 참가자) reaper는 이번 주기에 건드리지 않는다.
func TestSharedSkipsWhenLocked(t *testing.T) {
	rt := &fakeContainers{}
	store := &fakeStore{
		shared: map[string]*registry.SharedInstance{
			"web-01": {InstanceID: "c-1", IdleSince: time.Unix(1_000_000-1200, 0).UTC()}, // grace 지남
		},
		leaseCount: map[string]int64{"web-01": 0},
		locked:     map[string]bool{"web-01": true},
	}
	newReaper(rt, store).reap(context.Background())

	if len(rt.removed) != 0 || len(store.deletedShare) != 0 {
		t.Errorf("lock을 못 잡았는데 내림: removed=%v deleted=%v", rt.removed, store.deletedShare)
	}
	// lease 정리는 lock 전이라 돌아야 한다.
	if !store.pruned["web-01"] {
		t.Error("lock과 무관한 lease 정리까지 건너뜀")
	}
}
