package protocol

import "time"

// agent → dashboard. 모니터링 snapshot (Feature-01).
//
// agent가 Docker와 registry를 읽어서 만들고 ctf-redis의 MonitorSnapshotKey에 JSON으로 쓴다.
// dashboard는 읽어서 그대로 내려준다. agent는 listener를 열지 않기 때문에 이 키가 유일한 통로다.
//
// TTL을 거는 이유: agent가 죽으면 갱신이 멈추고 키가 사라진다.
// dashboard는 키가 없으면 "agent 응답 없음"으로 보여준다.
const (
	MonitorSnapshotKey = "monitor:snapshot"
	MonitorSnapshotTTL = 30 * time.Second
)

// 컨테이너 상태
const (
	MonitorRunning = "RUNNING" // 떠 있다
	MonitorExited  = "EXITED"  // 컨테이너는 있는데 멈췄다
	MonitorMissing = "MISSING" // registry에는 있는데 컨테이너가 없다
)

type MonitorSnapshot struct {
	GeneratedAt time.Time        `json:"generatedAt"`
	Summary     MonitorSummary   `json:"summary"`
	Shared      []MonitorShared  `json:"shared"`
	PerUser     []MonitorPerUser `json:"perUser"`
	Orphans     []MonitorOrphan  `json:"orphans"`
}

type MonitorSummary struct {
	Containers int `json:"containers"` // nimda.ctf 라벨 컨테이너 수 (멈춘 것 포함)
	Running    int `json:"running"`
	Shared     int `json:"shared"`
	PerUser    int `json:"perUser"`
	Leases     int `json:"leases"`
	Orphans    int `json:"orphans"`
}

// 문제마다 하나
type MonitorShared struct {
	ChallengeCode string         `json:"challengeCode"`
	InstanceID    string         `json:"instanceId"` // 앞 12자리
	ContainerName string         `json:"containerName,omitempty"`
	State         string         `json:"state"`
	HostPort      *int           `json:"hostPort"` // 멈췄거나 없으면 null
	CreatedAt     *time.Time     `json:"createdAt"`
	IdleSince     *time.Time     `json:"idleSince"` // lease가 0개가 된 시각. 사용 중이면 null
	Leases        []MonitorLease `json:"leases"`
}

type MonitorLease struct {
	UUID      string    `json:"uuid"`
	ExpiresAt time.Time `json:"expiresAt"`
	Expired   bool      `json:"expired"` // 만료 시각이 지났는데 reaper가 아직 안 지웠다
}

// uuid마다 하나. 만료 시각은 registry가 아직 저장하지 않아서 없다(ADR-0014).
type MonitorPerUser struct {
	UUID          string     `json:"uuid"`
	ChallengeCode string     `json:"challengeCode,omitempty"` // 컨테이너 라벨에서 읽는다
	InstanceID    string     `json:"instanceId"`
	ContainerName string     `json:"containerName,omitempty"`
	State         string     `json:"state"`
	HostPort      *int       `json:"hostPort"`
	CreatedAt     *time.Time `json:"createdAt"`
}

// nimda.ctf 라벨은 있는데 registry에 기록이 없는 컨테이너
type MonitorOrphan struct {
	InstanceID    string     `json:"instanceId"`
	ContainerName string     `json:"containerName"`
	ChallengeCode string     `json:"challengeCode,omitempty"`
	UUID          string     `json:"uuid,omitempty"`
	State         string     `json:"state"`
	CreatedAt     *time.Time `json:"createdAt"`
}
