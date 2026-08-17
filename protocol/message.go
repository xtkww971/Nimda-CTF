package protocol

type Type string

// RequestFrame의 Type
const (
	TypeCreate  Type = "create"
	TypeDestroy Type = "destroy"
	TypeStatus  Type = "status"
	TypeList    Type = "list"
)

// provisionor -> agent
type RequestFrame struct {
	Type    Type            `json:"type"`
	Create  *CreateRequest  `json:"create,omitempty"`
	Destroy *DestroyRequest `json:"destroy,omitempty"`
	Status  *StatusRequest  `json:"status,omitempty"`
	List    *ListRequest    `json:"list,omitempty"`
}

// agent -> provisionor
type ResponseFrame struct {
	Type    Type           `json:"type"`
	Create  *CreateResult  `json:"create,omitempty"`
	Destroy *DestroyResult `json:"destroy,omitempty"`
	Status  *StatusResult  `json:"status,omitempty"`
	List    *ListResult    `json:"list,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// ── Create ────────────────────────────────────────────────────────────────
// 생성 요청시 문제 파일을 주고 받지 않고 provisioner가 만든 폴더에 접근한 뒤 인스턴스를 생성
type CreateRequest struct {
	RequestID     string `json:"requestId"`
	ChallengeCode string `json:"challengeCode"`
	UserID        int64  `json:"userId"`        // 유저마다 별도의 인스턴스 제공을 위해 필요 - challengeCode_userId
	IsolationType string `json:"isolationType"` // SHARED | PER_USER
	FlagType      string `json:"flagType"`      // STATIC | DYNAMIC
	Flag          string `json:"flag,omitempty"`
}

// Create Status
const (
	StatusReady  = "READY"
	StatusFailed = "FAILED"
)

type CreateResult struct {
	RequestID  string `json:"requestId"`
	Status     string `json:"status"` // StatusReady | StatusFailed
	InstanceID string `json:"instanceId,omitempty"`
	Host       string `json:"host,omitempty"`      // 실패시 empty
	Port       int    `json:"port,omitempty"`      // 실패시 0
	ExpiresAt  string `json:"expiresAt,omitempty"` // RFC3339; 만료되지 않는 경우 empty
	Message    string `json:"message,omitempty"`
}

// ── Destroy ───────────────────────────────────────────────────────────────
// Agent에게 인스턴스 down 요청시 사용
// 결과로 CreateResult를 반환하고 ChallengeCode/UserID는 로그로 기록
type DestroyRequest struct {
	RequestID     string `json:"requestId"`
	InstanceID    string `json:"instanceId"`
	ChallengeCode string `json:"challengeCode,omitempty"`
	UserID        int64  `json:"userId,omitempty"`
}

// DestroyResult Status
const (
	DestroyDone     = "DESTROYED" // 인스턴스 해제
	DestroyNotFound = "NOT_FOUND" // 인스턴스를 찾을 수 없음
	DestroyFailed   = "FAILED"    // 인스턴스 파괴에 실패 - Message에 사유가 저장
)

type DestroyResult struct {
	RequestID string `json:"requestId"`
	Status    string `json:"status"` // DestroyDone | DestroyNotFound | DestroyFailed
	Message   string `json:"message,omitempty"`
}

// ── Status ────────────────────────────────────────────────────────────────
// 인스턴스 상태 조회
type StatusRequest struct {
	RequestID  string `json:"requestId"`
	InstanceID string `json:"instanceId"`
}

// Status state values.
const (
	StateRunning  = "RUNNING"
	StateStopped  = "STOPPED"
	StateNotFound = "NOT_FOUND"
)

type StatusResult struct {
	RequestID  string `json:"requestId"`
	InstanceID string `json:"instanceId"`
	State      string `json:"state"`               // StateRunning | StateStopped | StateNotFound
	Host       string `json:"host,omitempty"`      // set when RUNNING
	Port       int    `json:"port,omitempty"`      // set when RUNNING
	ExpiresAt  string `json:"expiresAt,omitempty"` // RFC3339 if it expires
	Message    string `json:"message,omitempty"`
}

// ── List ──────────────────────────────────────────────────────────────────

// ListRequest asks the Agent for every instance it is currently running, so
// the Provisioner can see the ports in use in real time.
type ListRequest struct {
	RequestID string `json:"requestId"`
}

// InstanceInfo describes one running instance.
type InstanceInfo struct {
	InstanceID    string `json:"instanceId"`
	ChallengeCode string `json:"challengeCode"`
	UserID        int64  `json:"userId"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	State         string `json:"state"` // StateRunning
}

type ListResult struct {
	RequestID string         `json:"requestId"`
	Instances []InstanceInfo `json:"instances"`
}
