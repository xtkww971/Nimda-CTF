package judge

// Verdict는 채점 판정 결과입니다. (백준/코드포스 스타일)
type Verdict string

const (
	VerdictAC  Verdict = "AC"  // Accepted
	VerdictWA  Verdict = "WA"  // Wrong Answer
	VerdictTLE Verdict = "TLE" // Time Limit Exceeded
	VerdictMLE Verdict = "MLE" // Memory Limit Exceeded
	VerdictRE  Verdict = "RE"  // Runtime Error
	VerdictCE  Verdict = "CE"  // Compile Error
	VerdictOLE Verdict = "OLE" // Output Limit Exceeded
	VerdictIE  Verdict = "IE"  // Internal Error (채점 서버/샌드박스 자체 문제, 유저 잘못 아님)
)

// TestCase는 S3 등에서 내려받은 개별 테스트케이스입니다.
// 나중에 S3 로더가 이 구조체를 채워주기만 하면 됩니다.
type TestCase struct {
	Index          int    // 테스트케이스 번호 (로깅/부분점수용)
	Input          []byte // stdin으로 넣을 내용
	ExpectedOutput []byte // 기대 출력
}

// ProblemLimits는 문제별 제한값입니다. (S3에서 문제 메타데이터로 받아올 값)
type ProblemLimits struct {
	TimeLimitSec  float64
	MemoryLimitKB int
}

// Submission은 Redis에서 받아올 채점 요청입니다.
type Submission struct {
	SubmissionID string
	ProblemID    string
	Language     string // "go", "python3", "cpp17" 등 Language 맵의 키와 매칭
	SourceCode   string
}

// TestCaseResult는 테스트케이스 1개에 대한 실행/판정 결과입니다.
type TestCaseResult struct {
	Index    int
	Verdict  Verdict
	TimeSec  float64
	MemoryKB int
	// Stdout/Stderr 일부는 디버깅/사용자 피드백용으로 잘라서 보관 (전체 저장은 비용 큼)
	StdoutSnippet string
	Message       string
}

// JudgeResult는 제출 하나에 대한 최종 채점 결과입니다.
// Redis로 API 서버에 돌려줄 응답의 payload가 됩니다.
type JudgeResult struct {
	SubmissionID string
	Verdict      Verdict // 전체 판정 (가장 먼저 실패한 테스트케이스 기준, 혹은 전부 통과시 AC)
	TimeSec      float64 // 전체 테스트케이스 중 최대 실행 시간
	MemoryKB     int     // 전체 테스트케이스 중 최대 메모리 사용량
	TestCases    []TestCaseResult
	CompileMsg   string // CE일 경우 컴파일러 에러 메시지
}
