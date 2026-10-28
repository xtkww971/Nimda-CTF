package protocol

// provisioner ↔ agent 주고받는 메시지.
const (
	ActionCreate  = "CREATE"
	ActionDestroy = "DESTROY"
)

type IsolationType string

const (
	IsolationShared  IsolationType = "SHARED"   // 공유 컨테이너
	IsolationPerUser IsolationType = "PER_USER" // 요청 당 컨테이너 생성
)

// 모르는 값을 SHARED로 흘려보내면 pwn 문제가 공용 컨테이너로 뜬다.
func (t IsolationType) Valid() bool {
	return t == IsolationShared || t == IsolationPerUser
}

// 참가자가 인스턴스에 어떻게 붙는지. agent는 문제 유형(category)을 모르고 이 값만 본다.
// 이 값에 따라 붙이는 네트워크와 포트를 밖에 여는지가 갈린다(ADR-0020).
type AccessType string

const (
	AccessHTTP AccessType = "HTTP" // 웹 프록시를 거친다. 포트를 밖에 열지 않는다
	AccessTCP  AccessType = "TCP"  // 포트 풀에서 포트를 받아 밖에 연다 (pwn)
)

// 모르는 값을 TCP로 흘려보내면 의도치 않게 포트가 밖에 열린다.
func (t AccessType) Valid() bool {
	return t == AccessHTTP || t == AccessTCP
}

const (
	FlagStatic  = "STATIC"
	FlagDynamic = "DYNAMIC"
)

// Provisioner → agent.
type RequestMessage struct {
	UUID   string `json:"uuid"`
	Action string `json:"action"`

	// CREATE
	ChallengeCode string `json:"challengeCode,omitempty"`
	IsolationType IsolationType `json:"isolationType,omitempty"`
	AccessType    AccessType    `json:"accessType,omitempty"` // AccessHTTP | AccessTCP
	FlagType      string        `json:"flagType,omitempty"` // FlagStatic | FlagDynamic
	Flag          string `json:"flag,omitempty"`

	// Provisioner가 문제 데이터를 읽어와 압축 해제한 경로
	BuildContextPath string `json:"buildContextPath,omitempty"`
}

// 처리 결과
const (
	StatusReady     = "READY"     // 인스턴스가 떠서 접속할 수 있다
	StatusFailed    = "FAILED"    // 실패. Message에 사유
	StatusDestroyed = "DESTROYED" // 인스턴스를 내렸다
	StatusNotFound  = "NOT_FOUND" // 내릴 인스턴스를 찾지 못했다
)

// Agent → Provisioner.
// 어떤 요청에 대한 응답인지는 UUID로 맞춘다.
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
