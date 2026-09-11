package protocol

// provisioner ↔ agent 주고받는 메시지.
const (
	ActionCreate  = "CREATE"
	ActionDestroy = "DESTROY"
)

// Provisioner → agent.
//
// 채워지는 필드는 Action에 따라 다르다. CREATE는 빌드에 필요한 것이 전부 있어야 하고,
// DESTROY는 UUID만 있으면 된다 — 어느 컨테이너인지는 에이전트가 registry에서 찾는다.
type RequestMessage struct {
	UUID   string `json:"uuid"`
	Action string `json:"action"`

	// 아래는 CREATE에서만 쓴다.

	// 이미지·컨테이너 이름에 쓴다. 없으면 무엇을 빌드할지 알 수 없다.
	ChallengeCode string `json:"challengeCode,omitempty"`
	IsolationType string `json:"isolationType,omitempty"` // SHARED | PER_USER
	FlagType      string `json:"flagType,omitempty"`      // STATIC | DYNAMIC
	Flag          string `json:"flag,omitempty"`

	// 조율자가 S3에서 받아 압축을 푼 폴더.
	BuildContextPath string `json:"buildContextPath,omitempty"`
}

// 처리 결과 상태.
const (
	StatusReady     = "READY"     // 인스턴스가 떠서 접속할 수 있다
	StatusFailed    = "FAILED"    // 실패. Message에 사유
	StatusDestroyed = "DESTROYED" // 인스턴스를 내렸다
	StatusNotFound  = "NOT_FOUND" // 내릴 인스턴스를 찾지 못했다
)

// Agent → Provisioner.
// 어떤 요청에 대한 응답인지는 UUID로 맞춘다. Action을 같이 싣는 이유는 받는 쪽이
// "무엇의 결과인지"를 알아야 결과를 어떻게 다룰지 정할 수 있기 때문이다.
// Host/Port/ExpiresAt은 CREATE가 성공했을 때만 찬다.
type ResponseMessage struct {
	UUID   string `json:"uuid"`
	Action string `json:"action"`
	Status string `json:"status"`

	InstanceID string `json:"instanceId,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"` // RFC3339. 만료가 없으면 빈 값
	Message    string `json:"message,omitempty"`   // 실패 사유
}

// ── 아래는 스트림에 오르지 않는다 ──────────────────────────────────────────
// 에이전트가 지금 떠 있는 인스턴스를 조회할 때 쓰는 타입들이다. 조회를 어떤 경로로
// 노출할지는 아직 정하지 않았다(ADR-0012의 "즉답 조회가 어색해진다").

type StatusRequest struct {
	UUID       string `json:"uuid"`
	InstanceID string `json:"instanceId"`
}

// Status 상태값
const (
	StateRunning  = "RUNNING"
	StateStopped  = "STOPPED"
	StateNotFound = "NOT_FOUND"
)

type StatusResult struct {
	UUID       string `json:"uuid"`
	InstanceID string `json:"instanceId"`
	State      string `json:"state"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
	Message    string `json:"message,omitempty"`
}
