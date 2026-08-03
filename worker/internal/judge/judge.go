package judge

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"judge-server/internal/isolate"
)

// sigXFSZ는 리눅스(x86/x86_64 및 대부분 아키텍처)에서 SIGXFSZ(파일 크기 제한 초과) 시그널 번호입니다.
// --fsize를 넘겨 출력하면 커널이 이 시그널로 프로세스를 죽입니다.
const sigXFSZ = 25

// Judger는 채점 파이프라인을 실행하는 주체입니다.
// BoxPool을 주입받아 동시에 여러 채점을 처리할 수 있습니다.
type Judger struct {
	Pool *isolate.BoxPool

	// UseCgroups는 isolate 박스에 cgroup 기반 자원 측정을 쓸지 여부입니다.
	// isolate.conf에서 cgroup이 켜져 있어야 true로 써야 합니다.
	UseCgroups bool

	// OutputLimitKB는 프로그램이 표준출력으로 뱉을 수 있는 최대 용량입니다.
	// 무한 출력(예: while True: print(...))으로 디스크를 채우는 것을 방지합니다.
	OutputLimitKB int

	// StopOnFirstFail이 true면 첫 실패 테스트케이스에서 채점을 중단합니다 (일반적인 ICPC 스타일).
	// false면 모든 테스트케이스를 끝까지 돌려 결과를 다 모읍니다.
	StopOnFirstFail bool

	// MetaDir은 isolate가 결과 meta 파일을 쓸 임시 디렉토리입니다. (박스 밖, 채점 서버 로컬)
	MetaDir string
}

// NewJudger는 기본값으로 Judger를 생성합니다.
func NewJudger(pool *isolate.BoxPool) *Judger {
	return &Judger{
		Pool:            pool,
		UseCgroups:      false,
		OutputLimitKB:   64 * 1024, // 64MB
		StopOnFirstFail: true,
		MetaDir:         "/tmp/judge-meta",
	}
}

// JudgeSubmission은 제출 하나를 처음부터 끝까지 채점하는 메인 엔트리포인트입니다.
// Redis에서 Submission을, S3에서 limits/testCases를 채워서 넘겨주면 됩니다.
func (j *Judger) JudgeSubmission(sub Submission, limits ProblemLimits, testCases []TestCase) (*JudgeResult, error) {
	lang, err := GetLanguage(sub.Language)
	if err != nil {
		return nil, err
	}

	box, err := j.Pool.Acquire(j.UseCgroups)
	if err != nil {
		return nil, fmt.Errorf("샌드박스 획득 실패: %w", err)
	}
	defer j.Pool.Release(box) // 성공/실패 무관하게 반드시 정리

	if err := os.MkdirAll(j.MetaDir, 0755); err != nil {
		return nil, fmt.Errorf("meta 디렉토리 생성 실패: %w", err)
	}

	// 1. 소스코드 박스에 기록
	if err := box.WriteFile(lang.SourceFileName, []byte(sub.SourceCode), 0644); err != nil {
		return nil, fmt.Errorf("소스코드 기록 실패: %w", err)
	}

	// 2. 컴파일 (필요한 언어만)
	if lang.CompileCmd != nil {
		compileMsg, ok, err := j.compile(box, lang, sub.SubmissionID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &JudgeResult{
				SubmissionID: sub.SubmissionID,
				Verdict:      VerdictCE,
				CompileMsg:   compileMsg,
			}, nil
		}
	}

	// 3. 테스트케이스별 실행 + 비교
	result := &JudgeResult{
		SubmissionID: sub.SubmissionID,
		Verdict:      VerdictAC,
		TestCases:    make([]TestCaseResult, 0, len(testCases)),
	}

	for _, tc := range testCases {
		tcResult, err := j.runTestCase(box, lang, limits, tc, sub.SubmissionID)
		if err != nil {
			return nil, fmt.Errorf("테스트케이스 #%d 실행 실패: %w", tc.Index, err)
		}

		result.TestCases = append(result.TestCases, *tcResult)
		if tcResult.TimeSec > result.TimeSec {
			result.TimeSec = tcResult.TimeSec
		}
		if tcResult.MemoryKB > result.MemoryKB {
			result.MemoryKB = tcResult.MemoryKB
		}

		if tcResult.Verdict != VerdictAC {
			result.Verdict = tcResult.Verdict
			if j.StopOnFirstFail {
				break
			}
		}
	}

	return result, nil
}

// compile은 박스 안에서 소스를 빌드합니다. 반환값 ok=false면 CE입니다.
func (j *Judger) compile(box *isolate.Box, lang Language, submissionID string) (message string, ok bool, err error) {
	metaPath := filepath.Join(j.MetaDir, fmt.Sprintf("%s-compile.meta", submissionID))
	stderrRel := "compile_stderr.txt"

	timeLimit := lang.CompileTimeLimitSec
	if timeLimit <= 0 {
		timeLimit = 10
	}
	memLimit := lang.CompileMemoryLimitKB
	if memLimit <= 0 {
		memLimit = 512 * 1024
	}

	cfg := isolate.RunConfig{
		Command:          lang.CompileCmd(),
		TimeLimitSec:     timeLimit,
		WallTimeLimitSec: timeLimit * 2,
		MemoryLimitKB:    memLimit,
		ProcessLimit:     16, // 컴파일러는 내부적으로 서브프로세스를 띄우는 경우가 많음 (cc1, ld 등)
		StderrPath:       stderrRel,
		MetaPath:         metaPath,
		FullEnv:          true, // 컴파일러가 자기 표준 경로들을 찾을 수 있게
		Env: map[string]string{
			"GOCACHE": "/box/.gocache",
			"HOME":    "/box",
		},
	}

	res, err := box.Run(cfg)
	if err != nil {
		return "", false, fmt.Errorf("컴파일 실행 실패: %w", err)
	}
	defer os.Remove(metaPath)

	stderrContent, _ := box.ReadFile(stderrRel)

	if res.Status != isolate.StatusOK || res.ExitCode != 0 {
		return string(stderrContent), false, nil
	}
	return string(stderrContent), true, nil
}

// runTestCase는 테스트케이스 하나를 실행하고 채점합니다.
func (j *Judger) runTestCase(box *isolate.Box, lang Language, limits ProblemLimits, tc TestCase, submissionID string) (*TestCaseResult, error) {
	stdinRel := "input.txt"
	stdoutRel := "output.txt"
	stderrRel := "run_stderr.txt"
	metaPath := filepath.Join(j.MetaDir, fmt.Sprintf("%s-tc%d.meta", submissionID, tc.Index))
	defer os.Remove(metaPath)

	if err := box.WriteFile(stdinRel, tc.Input, 0644); err != nil {
		return nil, fmt.Errorf("입력 파일 기록 실패: %w", err)
	}

	// Go/JVM처럼 시작시 가상주소공간을 크게 선점하는 언어는, cgroup 없이 --mem(RLIMIT_AS)을
	// 걸면 실제 메모리를 쓰기도 전에 즉시 죽어버린다. 이 경우 cgroup 모드가 아니라면
	// --mem 자체를 생략하고, 실행 후 실측 max-rss로만 MLE를 판정한다 (아래 스위치문의
	// "MaxRSSKB > limits.MemoryLimitKB" 케이스).
	memLimitKB := limits.MemoryLimitKB
	if lang.SkipVirtualMemLimit && !j.UseCgroups {
		memLimitKB = 0
	}

	processLimit := lang.RunProcessLimit
	if processLimit <= 0 {
		processLimit = 1
	}

	cfg := isolate.RunConfig{
		Command:          lang.RunCmd(),
		TimeLimitSec:     limits.TimeLimitSec,
		WallTimeLimitSec: limits.TimeLimitSec*2 + 1, // 무한 sleep 등으로 CPU time은 안 쓰면서 시간만 끄는 경우 방지
		ExtraTimeSec:     0.5,                       // TLE 경계에서 오차로 인한 오탐 방지용 유예
		MemoryLimitKB:    memLimitKB,
		ProcessLimit:     processLimit,
		Env:              lang.RunEnv,
		FileSizeLimitKB:  j.OutputLimitKB,
		StdinPath:        stdinRel,
		StdoutPath:       stdoutRel,
		StderrPath:       stderrRel,
		MetaPath:         metaPath,
	}

	res, err := box.Run(cfg)
	if err != nil {
		return nil, err
	}

	result := &TestCaseResult{
		Index:    tc.Index,
		TimeSec:  res.TimeSec,
		MemoryKB: res.MaxRSSKB,
	}

	// 판정 우선순위: 내부오류 > TLE > MLE > RE > OLE > WA/AC
	switch {
	case res.Status == isolate.StatusInternal:
		result.Verdict = VerdictIE
		result.Message = res.Message

	case res.Status == isolate.StatusTimeOut:
		result.Verdict = VerdictTLE

	case res.MaxRSSKB > 0 && limits.MemoryLimitKB > 0 && res.MaxRSSKB > limits.MemoryLimitKB:
		// isolate가 --mem으로 즉시 죽이지 못하고 살아남았더라도, 실측 메모리가 제한을 넘겼으면 MLE 처리.
		// cgroup(--cg-mem) 사용시엔 OOM killer가 SIGKILL로 죽이므로 CgOOMKill도 함께 체크.
		result.Verdict = VerdictMLE

	case res.CgOOMKill:
		result.Verdict = VerdictMLE

	case res.Status == isolate.StatusSignal && res.ExitSig == sigXFSZ:
		// --fsize 제한을 넘겨서 커널이 SIGXFSZ(25)로 죽인 경우. RE와 구분해서 OLE로 분류.
		result.Verdict = VerdictOLE

	case (res.Status == isolate.StatusSignal || res.Status == isolate.StatusRunErr) && j.outputExceeded(box, stdoutRel):
		// 언어에 따라 SIGXFSZ를 시그널로 받지 않고(예: Python은 인터프리터 셧다운 중
		// flush 실패를 자체적으로 잡아 일반 에러 종료코드로 바꿔버림) 그냥 비정상
		// 종료코드로만 나타나는 경우가 있다. 이럴 땐 실제 stdout 파일 크기를 제한과
		// 비교해서 OLE 여부를 다시 한번 확인한다.
		result.Verdict = VerdictOLE

	case res.Status == isolate.StatusSignal || res.Status == isolate.StatusRunErr:
		result.Verdict = VerdictRE
		result.Message = fmt.Sprintf("exit_code=%d exit_signal=%d msg=%s", res.ExitCode, res.ExitSig, res.Message)

	default:
		// 정상 종료 -> 출력 비교
		actual, readErr := box.ReadFile(stdoutRel)
		if readErr != nil {
			actual = []byte{}
		}
		if compareOutput(actual, tc.ExpectedOutput) {
			result.Verdict = VerdictAC
		} else {
			result.Verdict = VerdictWA
		}
		result.StdoutSnippet = snippet(actual, 500)
	}

	return result, nil
}

// outputExceeded는 stdout 파일의 실제 크기가 출력 제한을 넘었는지 확인합니다.
// isolate의 --fsize(SIGXFSZ)가 언어/런타임에 따라 시그널이 아닌 일반 종료코드로
// 감춰지는 경우를 보완하기 위한 이중 체크입니다.
func (j *Judger) outputExceeded(box *isolate.Box, stdoutRel string) bool {
	if j.OutputLimitKB <= 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(box.Path, stdoutRel))
	if err != nil {
		return false
	}
	return info.Size() >= int64(j.OutputLimitKB)*1024
}

// compareOutput은 출력 비교 규칙입니다.
// 기본은 "줄 끝 공백/개행, 파일 끝 개행 차이는 무시"하는 표준적인 스페셜 저지 없는 비교입니다.
// 문제에 따라 더 엄격하게(exact) 또는 느슨하게(토큰 단위) 바꾸고 싶다면 이 함수만 교체하면 됩니다.
func compareOutput(actual, expected []byte) bool {
	return normalizeOutput(actual) == normalizeOutput(expected)
}

func normalizeOutput(b []byte) string {
	// CRLF -> LF 통일
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))

	lines := bytes.Split(b, []byte("\n"))
	trimmed := make([][]byte, 0, len(lines))
	for _, line := range lines {
		trimmed = append(trimmed, bytes.TrimRight(line, " \t"))
	}
	// 끝의 빈 줄들 제거 (파일 끝 개행 유무 차이 무시)
	for len(trimmed) > 0 && len(trimmed[len(trimmed)-1]) == 0 {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return string(bytes.Join(trimmed, []byte("\n")))
}

func snippet(b []byte, maxLen int) string {
	if len(b) <= maxLen {
		return string(b)
	}
	return string(b[:maxLen]) + "...(truncated)"
}
