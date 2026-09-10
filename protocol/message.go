package protocol

// Privisioner - Agent 사용 메시지
// ── Create ────────────────────────────────────────────────────────────────
// 생성 요청시 문제 파일을 주고 받지 않고 provisioner가 만든 폴더에 접근한 뒤 인스턴스를 생성.
// Provisioner가 S3에서 받아 압축을 푼 폴더 경로(BuildContextPath)를 넘기면, Agent는 그
// 경로에서 바로 빌드한다. 두 프로세스는 그 폴더를 공유(공유 볼륨)하며, 압축은 한 번만 푼다.

// CreateRequest는 ChallengeCategory가 필요 없기에 넣지 않는다.
type CreateRequest struct {
	RequestID     string `json:"requestId"`
	ChallengeCode string `json:"challengeCode"`
	UserID        int64  `json:"userId"`        // 유저마다 별도의 인스턴스 제공을 위해 필요 - challengeCode_userId
	IsolationType string `json:"isolationType"` // SHARED | PER_USER
	FlagType      string `json:"flagType"`      // STATIC | DYNAMIC
	Flag          string `json:"flag,omitempty"`

	// 압축 해체 폴더 위치
	BuildContextPath string `json:"buildContextPath"`
}

// Create Status
const (
	StatusReady      = "READY"
	StatusProcessing = "PROCESSING"
	StatusFailed     = "FAILED"
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

// Status 상태값
const (
	StateRunning  = "RUNNING"
	StateStopped  = "STOPPED"
	StateNotFound = "NOT_FOUND"
)

type StatusResult struct {
	RequestID  string `json:"requestId"`
	InstanceID string `json:"instanceId"`
	State      string `json:"state"`               // StateRunning | StateStopped | StateNotFound
	Host       string `json:"host,omitempty"`      // RUNNING일 때 채워짐
	Port       int    `json:"port,omitempty"`      // RUNNING일 때 채워짐
	ExpiresAt  string `json:"expiresAt,omitempty"` // 만료되면 RFC3339
	Message    string `json:"message,omitempty"`
}

// ── List ──────────────────────────────────────────────────────────────────

// 지금 떠 있는 인스턴스를 전부 물어보는 요청. provisioner가 쓰는 포트를 실시간으로 보려고 쓴다.
type ListRequest struct {
	RequestID string `json:"requestId"`
}

// 떠 있는 인스턴스 하나의 정보.
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
