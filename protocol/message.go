package protocol

// 조율자 ↔ 에이전트가 주고받는 페이로드 타입.
//
// 예전에는 이 타입들이 RequestFrame/ResponseFrame이라는 봉투에 담겨 유닉스 소켓 위의
// 길이 접두 JSON 프레임으로 오갔다. 지금은 Redis Stream이라 봉투가 없다 — 스트림
// 메시지는 평평한 문자열 맵이고, 요청 종류는 스트림으로 나눠 표현한다
// (stream.go, ADR-0012). 남은 건 그 안에 실리는 알맹이뿐이다.
//
// 식별자는 UUID 하나다. 웹이 사용자마다 발급해 내려보내는 값이고, 조율자는 그걸 그대로
// 에이전트까지 흘린다. 예전에는 requestId(요청 식별)와 userId(사용자 식별)가 따로
// 있었지만, 웹이 UUID ↔ 사용자 매핑을 자기 쪽 redis에서 관리하기로 하면서
// CTF 서버는 사용자가 누구인지 알 필요가 없어졌다.
//
// json 태그는 그대로 둔다. 스트림 변환(ToStreamValues/Parse*)은 태그를 안 보지만,
// 로그나 디버깅에서 이 구조체를 통째로 찍을 때 쓰인다.

// ── Create ────────────────────────────────────────────────────────────────
// 생성 요청시 문제 파일을 주고 받지 않고 조율자가 만든 폴더에 접근한 뒤 인스턴스를 생성.
// 조율자가 S3에서 받아 압축을 푼 폴더 경로(BuildContextPath)를 넘기면, 에이전트는 그
// 경로에서 바로 빌드한다. 두 프로세스는 그 폴더를 공유(공유 볼륨)하며, 압축은 한 번만 푼다.
type CreateRequest struct {
	UUID          string `json:"uuid"`
	ChallengeCode string `json:"challengeCode"` // 이미지·컨테이너 이름에 쓴다
	IsolationType string `json:"isolationType"` // SHARED | PER_USER
	FlagType      string `json:"flagType"`      // STATIC | DYNAMIC
	Flag          string `json:"flag,omitempty"`

	// 압축 해제 폴더 위치
	BuildContextPath string `json:"buildContextPath"`
}

// Create Status
const (
	StatusReady  = "READY"
	StatusFailed = "FAILED"
)

// 에이전트가 돌려주는 생성 결과.
//
// 예전에는 이 위에 AgentResult라는 봉투를 덧씌워 challengeCode/userId를 되실어 보냈다.
// 결과만 받아서는 "어느 문제의 누구였는지"를 복원할 수 없었기 때문인데, 이제 웹이 보는
// 결과도 UUID로만 식별하므로 되실을 것이 없다. 봉투가 사라졌다.
type CreateResult struct {
	UUID       string `json:"uuid"`
	Status     string `json:"status"` // StatusReady | StatusFailed
	InstanceID string `json:"instanceId,omitempty"`
	Host       string `json:"host,omitempty"`      // 실패시 empty
	Port       int    `json:"port,omitempty"`      // 실패시 0
	ExpiresAt  string `json:"expiresAt,omitempty"` // RFC3339; 만료되지 않는 경우 empty
	Message    string `json:"message,omitempty"`
}

// ── Destroy ───────────────────────────────────────────────────────────────
// 에이전트에게 인스턴스 down 요청시 사용
type DestroyRequest struct {
	UUID       string `json:"uuid"`
	InstanceID string `json:"instanceId"`
	// 삭제 자체엔 필요 없지만 로그로 남기려고 싣는다.
	ChallengeCode string `json:"challengeCode,omitempty"`
}

// DestroyResult Status
const (
	DestroyDone     = "DESTROYED" // 인스턴스 해제
	DestroyNotFound = "NOT_FOUND" // 인스턴스를 찾을 수 없음
	DestroyFailed   = "FAILED"    // 인스턴스 파괴에 실패 - Message에 사유가 저장
)

type DestroyResult struct {
	UUID    string `json:"uuid"`
	Status  string `json:"status"` // DestroyDone | DestroyNotFound | DestroyFailed
	Message string `json:"message,omitempty"`
}

// ── Status ────────────────────────────────────────────────────────────────
// 인스턴스 상태 조회
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
	State      string `json:"state"`               // StateRunning | StateStopped | StateNotFound
	Host       string `json:"host,omitempty"`      // RUNNING일 때 채워짐
	Port       int    `json:"port,omitempty"`      // RUNNING일 때 채워짐
	ExpiresAt  string `json:"expiresAt,omitempty"` // 만료되면 RFC3339
	Message    string `json:"message,omitempty"`
}

// ── List ──────────────────────────────────────────────────────────────────

// 지금 떠 있는 인스턴스를 전부 물어보는 요청.
type ListRequest struct {
	UUID string `json:"uuid"`
}

// 떠 있는 인스턴스 하나의 정보.
type InstanceInfo struct {
	InstanceID    string `json:"instanceId"`
	UUID          string `json:"uuid"`
	ChallengeCode string `json:"challengeCode"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	State         string `json:"state"` // StateRunning
}

type ListResult struct {
	UUID      string         `json:"uuid"`
	Instances []InstanceInfo `json:"instances"`
}
