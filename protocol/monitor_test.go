package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// agent가 쓰고 dashboard가 읽는 JSON이다. 한 바퀴 돌려도 값이 그대로여야 한다.
func TestMonitorSnapshotRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 23, 5, 10, 0, 0, time.UTC)
	port := 53974
	in := MonitorSnapshot{
		GeneratedAt: now,
		Summary:     MonitorSummary{Containers: 1, Running: 1, Shared: 1, Leases: 1},
		Shared: []MonitorShared{{
			ChallengeCode: "web-basic-01", InstanceID: "8ea72b98f32a", State: MonitorRunning,
			HostPort: &port, CreatedAt: &now,
			Leases: []MonitorLease{{UUID: "u-1", ExpiresAt: now.Add(time.Hour)}},
		}},
		PerUser: []MonitorPerUser{},
		Orphans: []MonitorOrphan{},
	}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out MonitorSnapshot
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if *out.Shared[0].HostPort != port || out.Shared[0].Leases[0].UUID != "u-1" || !out.GeneratedAt.Equal(now) {
		t.Errorf("round trip changed the snapshot: %+v", out)
	}
}

// 화면이 "값 없음"과 0번 포트를 헷갈리지 않게, 없는 값은 null로 나가야 한다.
func TestMonitorSnapshotNullFields(t *testing.T) {
	in := MonitorSnapshot{
		Shared:  []MonitorShared{{ChallengeCode: "c", State: MonitorMissing, Leases: []MonitorLease{}}},
		PerUser: []MonitorPerUser{},
		Orphans: []MonitorOrphan{},
	}
	b, _ := json.Marshal(in)
	s := string(b)
	for _, want := range []string{`"hostPort":null`, `"idleSince":null`, `"createdAt":null`} {
		if !strings.Contains(s, want) {
			t.Errorf("%s가 없음: %s", want, s)
		}
	}
}
