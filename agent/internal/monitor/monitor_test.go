package monitor

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"nimda-ctf-agent/internal/registry"
	"nimda-ctf-agent/internal/runtime"
	protocol "nimda-ctf-protocol"
)

type fakeContainers []runtime.ContainerInfo

func (f fakeContainers) List(context.Context) ([]runtime.ContainerInfo, error) { return f, nil }

type fakeRegistry struct {
	entries map[string]*registry.Entry
	shared  map[string]*registry.SharedInstance
	leases  map[string][]registry.Lease
}

func (f fakeRegistry) AllEntries(context.Context) (map[string]*registry.Entry, error) {
	return f.entries, nil
}
func (f fakeRegistry) AllShared(context.Context) (map[string]*registry.SharedInstance, error) {
	return f.shared, nil
}
func (f fakeRegistry) Leases(_ context.Context, code string) ([]registry.Lease, error) {
	return f.leases[code], nil
}

var now = time.Date(2026, 10, 23, 5, 10, 0, 0, time.UTC)

const (
	sharedID  = "aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111"
	perUserID = "bbbbbbbbbbbb2222222222222222222222222222222222222222222222222222"
	orphanID  = "cccccccccccc3333333333333333333333333333333333333333333333333333"
	deadID    = "dddddddddddd4444444444444444444444444444444444444444444444444444"
)

func build(t *testing.T, c fakeContainers, r fakeRegistry) *protocol.MonitorSnapshot {
	t.Helper()
	m := New(c, r, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.now = func() time.Time { return now }
	snap, err := m.Build(context.Background())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return snap
}

// 기본 상황: SHARED 1개(lease 2개, 하나는 만료), PER_USER 1개, orphan 1개
func TestBuildSnapshot(t *testing.T) {
	containers := fakeContainers{
		{ID: sharedID, Name: "ctf-web-01", Running: true, HostPort: 53974, Created: now.Add(-time.Hour),
			Labels: map[string]string{labelCode: "web-01"}},
		{ID: perUserID, Name: "ctf-web-02-u1", Running: true, HostPort: 57645, Created: now.Add(-time.Minute),
			Labels: map[string]string{labelCode: "web-02", labelUUID: "u1"}},
		{ID: orphanID, Name: "ctf-old", Running: false, Created: now.Add(-24 * time.Hour),
			Labels: map[string]string{labelCode: "old", labelUUID: "u9"}},
	}
	reg := fakeRegistry{
		entries: map[string]*registry.Entry{
			"u1": {Isolation: protocol.IsolationPerUser, InstanceID: perUserID},
			"s1": {Isolation: protocol.IsolationShared, ChallengeCode: "web-01"},
			"s2": {Isolation: protocol.IsolationShared, ChallengeCode: "web-01"},
		},
		shared: map[string]*registry.SharedInstance{"web-01": {InstanceID: sharedID, CreatedAt: now.Add(-time.Hour)}},
		leases: map[string][]registry.Lease{"web-01": {
			{UUID: "s1", ExpiresAt: now.Add(-time.Minute)}, // 만료됨
			{UUID: "s2", ExpiresAt: now.Add(time.Hour)},
		}},
	}

	snap := build(t, containers, reg)

	want := protocol.MonitorSummary{Containers: 3, Running: 2, Shared: 1, PerUser: 1, Leases: 2, Orphans: 1}
	if snap.Summary != want {
		t.Errorf("summary = %+v, want %+v", snap.Summary, want)
	}

	s := snap.Shared[0]
	if s.State != protocol.MonitorRunning || s.HostPort == nil || *s.HostPort != 53974 || s.InstanceID != "aaaaaaaaaaaa" {
		t.Errorf("shared = %+v", s)
	}
	if !s.Leases[0].Expired || s.Leases[1].Expired {
		t.Errorf("lease 만료 판단이 틀림: %+v", s.Leases)
	}

	p := snap.PerUser[0]
	if p.UUID != "u1" || p.ChallengeCode != "web-02" || p.State != protocol.MonitorRunning || *p.HostPort != 57645 {
		t.Errorf("perUser = %+v", p)
	}

	o := snap.Orphans[0]
	if o.ContainerName != "ctf-old" || o.State != protocol.MonitorExited || o.UUID != "u9" {
		t.Errorf("orphan = %+v", o)
	}
}

// registry에는 있는데 컨테이너가 없으면 MISSING, 멈춰 있으면 EXITED. 둘 다 포트는 null이다.
func TestBuildMissingAndExited(t *testing.T) {
	containers := fakeContainers{
		{ID: deadID, Name: "ctf-web-02-u2", Running: false, HostPort: 0},
	}
	reg := fakeRegistry{
		entries: map[string]*registry.Entry{
			"u2": {Isolation: protocol.IsolationPerUser, InstanceID: deadID},
		},
		shared: map[string]*registry.SharedInstance{"web-01": {InstanceID: sharedID}},
		leases: map[string][]registry.Lease{},
	}

	snap := build(t, containers, reg)

	if s := snap.Shared[0]; s.State != protocol.MonitorMissing || s.HostPort != nil {
		t.Errorf("컨테이너 없는 SHARED = %+v, want MISSING", s)
	}
	if p := snap.PerUser[0]; p.State != protocol.MonitorExited || p.HostPort != nil {
		t.Errorf("멈춘 PER_USER = %+v, want EXITED", p)
	}
	if len(snap.Orphans) != 0 {
		t.Errorf("registry에 있는 컨테이너가 orphan으로 잡힘: %+v", snap.Orphans)
	}
}

// 아무것도 없으면 빈 배열이어야 한다. null이면 화면이 배열로 못 다룬다.
func TestBuildEmpty(t *testing.T) {
	snap := build(t, fakeContainers{}, fakeRegistry{
		entries: map[string]*registry.Entry{}, shared: map[string]*registry.SharedInstance{},
	})
	if snap.Shared == nil || snap.PerUser == nil || snap.Orphans == nil {
		t.Errorf("빈 목록이 nil: %+v", snap)
	}
	if snap.Summary != (protocol.MonitorSummary{}) {
		t.Errorf("summary = %+v, want 0", snap.Summary)
	}
}
