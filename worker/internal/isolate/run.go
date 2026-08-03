package isolate

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Status는 isolate 실행 후 meta 파일의 status 필드를 해석한 결과입니다.
type Status string

const (
	StatusOK       Status = "OK" // 정상 종료 (exit code와 무관하게 샌드박스 자체는 문제 없음)
	StatusRunErr   Status = "RE" // 0이 아닌 종료 코드로 종료
	StatusSignal   Status = "SG" // 시그널에 의해 죽음 (세그폴트 등)
	StatusTimeOut  Status = "TO" // 시간 초과 (time 또는 wall-time)
	StatusInternal Status = "XX" // isolate 자체 내부 오류 (박스/설정 문제)
)

// RunConfig는 isolate --run에 넘길 자원 제한 및 IO 설정입니다.
type RunConfig struct {
	// 실행할 커맨드. 예: []string{"/usr/bin/python3", "main.py"}
	// isolate 박스 루트 기준 상대/절대 경로여야 합니다.
	Command []string

	// 자원 제한
	TimeLimitSec     float64 // --time, CPU 시간
	WallTimeLimitSec float64 // --wall-time, 실제 경과 시간 (무한루프+sleep 방지용으로 TimeLimit보다 약간 여유)
	MemoryLimitKB    int     // --mem (cgroup 미사용시) 또는 --cg-mem (cgroup 사용시)
	StackLimitKB     int     // --stack, 0이면 mem limit과 동일하게 취급
	ProcessLimit     int     // --processes, 기본 1 (fork 금지)
	FileSizeLimitKB  int     // --fsize, 출력 파일 크기 제한 (0이면 isolate 기본값)
	ExtraTimeSec     float64 // --extra-time, TLE 판정 유예시간 (정확한 TLE 계산용)

	// 표준입출력 리다이렉션 (박스 기준 상대 경로 또는 절대 경로)
	StdinPath  string
	StdoutPath string
	StderrPath string

	// 환경변수를 그대로 물려줄지 (--full-env). 기본은 격리된 최소 환경.
	FullEnv bool
	// 추가로 넘길 환경변수 (--env=KEY=VALUE)
	Env map[string]string

	// meta 파일 경로 (박스 밖 임시 경로 권장, isolate가 직접 씀)
	MetaPath string
}

// RunResult는 meta 파일을 파싱한 결과 + 실제 stdout/stderr를 담습니다.
type RunResult struct {
	Status    Status
	ExitCode  int
	ExitSig   int
	TimeSec   float64 // CPU time
	WallSec   float64 // wall time
	MaxRSSKB  int
	Message   string // isolate가 남긴 메시지 (주로 XX/SG일 때)
	CgOOMKill bool   // cgroup 사용시 OOM killer에 의해 죽었는지
}

// Run은 박스 안에서 커맨드를 실행하고 결과를 meta 파일에서 읽어 반환합니다.
func (b *Box) Run(cfg RunConfig) (*RunResult, error) {
	if b.Path == "" {
		return nil, fmt.Errorf("box가 초기화되지 않았습니다 (Init()을 먼저 호출하세요)")
	}
	if cfg.MetaPath == "" {
		return nil, fmt.Errorf("MetaPath는 필수입니다")
	}

	args := b.baseArgs()

	if cfg.TimeLimitSec > 0 {
		args = append(args, fmt.Sprintf("--time=%s", formatSeconds(cfg.TimeLimitSec)))
	}
	if cfg.WallTimeLimitSec > 0 {
		args = append(args, fmt.Sprintf("--wall-time=%s", formatSeconds(cfg.WallTimeLimitSec)))
	}
	if cfg.ExtraTimeSec > 0 {
		args = append(args, fmt.Sprintf("--extra-time=%s", formatSeconds(cfg.ExtraTimeSec)))
	}
	if cfg.MemoryLimitKB > 0 {
		if b.UseCgroups {
			args = append(args, fmt.Sprintf("--cg-mem=%d", cfg.MemoryLimitKB))
		} else {
			args = append(args, fmt.Sprintf("--mem=%d", cfg.MemoryLimitKB))
		}
	}
	if cfg.StackLimitKB > 0 {
		args = append(args, fmt.Sprintf("--stack=%d", cfg.StackLimitKB))
	}

	processLimit := cfg.ProcessLimit
	if processLimit <= 0 {
		processLimit = 1
	}
	args = append(args, fmt.Sprintf("--processes=%d", processLimit))

	if cfg.FileSizeLimitKB > 0 {
		args = append(args, fmt.Sprintf("--fsize=%d", cfg.FileSizeLimitKB))
	}

	if cfg.StdinPath != "" {
		args = append(args, fmt.Sprintf("--stdin=%s", cfg.StdinPath))
	}
	if cfg.StdoutPath != "" {
		args = append(args, fmt.Sprintf("--stdout=%s", cfg.StdoutPath))
	}
	if cfg.StderrPath != "" {
		args = append(args, fmt.Sprintf("--stderr=%s", cfg.StderrPath))
	}

	if cfg.FullEnv {
		args = append(args, "--full-env")
	}
	for k, v := range cfg.Env {
		args = append(args, fmt.Sprintf("--env=%s=%s", k, v))
	}

	args = append(args, fmt.Sprintf("--meta=%s", cfg.MetaPath))
	args = append(args, "--run", "--")
	args = append(args, cfg.Command...)

	cmd := exec.Command(BinaryPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// isolate --run 자체의 종료 코드는 "채점 대상 프로그램"의 종료 코드가 아니라
	// isolate 실행 자체의 성패이므로, non-zero여도 meta 파일을 우선 신뢰하고 파싱합니다.
	_ = cmd.Run()

	result, err := parseMetaFile(cfg.MetaPath)
	if err != nil {
		return nil, fmt.Errorf("meta 파일 파싱 실패: %w (isolate stderr: %s)", err, stderr.String())
	}
	return result, nil
}

func formatSeconds(s float64) string {
	return strconv.FormatFloat(s, 'f', 3, 64)
}

// parseMetaFile은 isolate가 남긴 "key:value" 형식의 meta 파일을 파싱합니다.
func parseMetaFile(path string) (*RunResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	raw := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		raw[line[:idx]] = line[idx+1:]
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	result := &RunResult{Status: StatusOK}

	if v, ok := raw["status"]; ok && v != "" {
		result.Status = Status(v)
	}
	if v, ok := raw["exitcode"]; ok {
		result.ExitCode, _ = strconv.Atoi(v)
	}
	if v, ok := raw["exitsig"]; ok {
		result.ExitSig, _ = strconv.Atoi(v)
	}
	if v, ok := raw["time"]; ok {
		result.TimeSec, _ = strconv.ParseFloat(v, 64)
	}
	if v, ok := raw["time-wall"]; ok {
		result.WallSec, _ = strconv.ParseFloat(v, 64)
	}
	// cgroup 사용시엔 cg-mem, 아니면 max-rss
	if v, ok := raw["cg-mem"]; ok {
		result.MaxRSSKB, _ = strconv.Atoi(v)
	} else if v, ok := raw["max-rss"]; ok {
		result.MaxRSSKB, _ = strconv.Atoi(v)
	}
	if v, ok := raw["message"]; ok {
		result.Message = v
	}
	if v, ok := raw["cg-oom-killed"]; ok && v == "1" {
		result.CgOOMKill = true
	}

	return result, nil
}

// WriteFile은 박스 안에 소스코드/입력 파일 등을 씁니다.
// isolate는 박스 안(b.Path 아래 실제로는 "box" 서브디렉토리)에서 실행되므로,
// b.Path 기준 상대경로로 파일을 배치해야 --chdir 없이도 실행 파일을 찾습니다.
func (b *Box) WriteFile(relPath string, content []byte, perm os.FileMode) error {
	if b.Path == "" {
		return fmt.Errorf("box가 초기화되지 않았습니다")
	}
	full := filepath.Join(b.Path, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}
	return os.WriteFile(full, content, perm)
}

// ReadFile은 박스 안의 파일(주로 stdout 결과물)을 읽습니다.
func (b *Box) ReadFile(relPath string) ([]byte, error) {
	if b.Path == "" {
		return nil, fmt.Errorf("box가 초기화되지 않았습니다")
	}
	return os.ReadFile(filepath.Join(b.Path, relPath))
}
