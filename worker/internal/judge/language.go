package judge

import "fmt"

// Language는 언어별로 소스 파일명, 컴파일 커맨드, 실행 커맨드를 정의합니다.
// 경로는 모두 isolate 박스 루트(box.Path) 기준 상대경로입니다.
type Language struct {
	Name           string
	SourceFileName string
	// CompileCmd가 nil이면 인터프리터 언어로 취급해 컴파일 단계를 건너뜁니다.
	CompileCmd func() []string
	// RunCmd는 컴파일 산출물 또는 소스 파일을 실행하는 커맨드입니다.
	RunCmd func() []string

	// 컴파일 단계용 별도 자원 제한 (실행 제한보다 넉넉하게 주는 게 일반적)
	CompileTimeLimitSec  float64
	CompileMemoryLimitKB int

	// SkipVirtualMemLimit이 true면, cgroup을 안 쓰는 모드에서 --mem(RLIMIT_AS)을
	// 아예 걸지 않습니다. Go 런타임 등은 시작 시 가상주소공간을 아주 크게(수 GB~)
	// 미리 예약해두기 때문에, 문제의 실제 메모리 제한(보통 수백 MB)을 --mem에
	// 그대로 넣으면 프로그램이 즉시 죽어버립니다(실제 메모리를 전혀 안 썼는데도).
	// 이 경우 실행 후 실측 max-rss와 문제 제한을 비교해서 MLE를 판정하는
	// 방식(judge.go의 post-run 체크)에만 의존합니다.
	// ⚠️ 주의: 이 모드에서는 cgroup 없이는 메모리 강제가 느슨해집니다.
	// (커널 RLIMIT 차원의 즉시 차단이 없고, 실제 물리 메모리가 바닥날 때까지
	// 호스트 OOM killer에 맡겨지므로) 이런 언어는 cgroup 사용을 강력히 권장합니다.
	SkipVirtualMemLimit bool

	// RunProcessLimit은 실행 단계에서 허용할 프로세스/스레드 개수입니다.
	// 0이면 기본값 1(스레드 하나만 허용, fork 금지)을 씁니다.
	// Go처럼 런타임 자체가 스케줄러/GC용으로 여러 OS 스레드를 만드는 언어는
	// 반드시 1보다 크게 줘야 합니다 (안 그러면 정상적인 단일 스레드 프로그램도
	// 스레드 생성 실패로 RE가 납니다).
	RunProcessLimit int

	// RunEnv는 실행 단계에서 추가로 주입할 환경변수입니다.
	// 예: Go는 GOMAXPROCS=1로 스레드 생성을 최소화해서 RunProcessLimit을 낮게 유지.
	RunEnv map[string]string
}

// Languages는 지원하는 언어 목록입니다. 필요에 따라 계속 추가하면 됩니다.
var Languages = map[string]Language{
	"c11": {
		Name:           "c11",
		SourceFileName: "main.c",
		CompileCmd: func() []string {
			return []string{"/usr/bin/gcc", "-O2", "-std=c11", "-o", "main", "main.c", "-lm"}
		},
		RunCmd: func() []string {
			return []string{"./main"}
		},
		CompileTimeLimitSec:  10,
		CompileMemoryLimitKB: 512 * 1024,
	},
	"cpp17": {
		Name:           "cpp17",
		SourceFileName: "main.cpp",
		CompileCmd: func() []string {
			return []string{"/usr/bin/g++", "-O2", "-std=c++17", "-o", "main", "main.cpp"}
		},
		RunCmd: func() []string {
			return []string{"./main"}
		},
		CompileTimeLimitSec:  10,
		CompileMemoryLimitKB: 512 * 1024,
	},
	"python3": {
		Name:           "python3",
		SourceFileName: "main.py",
		CompileCmd:     nil, // 인터프리터 언어
		RunCmd: func() []string {
			return []string{"/usr/bin/python3", "main.py"}
		},
	},
	"java17": {
		Name:           "java17",
		SourceFileName: "Main.java",
		CompileCmd: func() []string {
			return []string{"/usr/bin/javac", "Main.java"}
		},
		RunCmd: func() []string {
			// JVM 자체 오버헤드가 크므로 MemoryLimit 산정시 여유를 더 줘야 함
			return []string{"/usr/bin/java", "-Xss64m", "Main"}
		},
		CompileTimeLimitSec:  20,
		CompileMemoryLimitKB: 1024 * 1024,
		SkipVirtualMemLimit:  true, // JVM도 힙/메타스페이스용 가상주소공간을 크게 예약함
		RunProcessLimit:      32,   // JVM은 GC/JIT 컴파일러용으로 스레드를 여러 개 만듦
	},
}

// GetLanguage는 language 문자열로 Language 정의를 찾습니다.
func GetLanguage(name string) (Language, error) {
	lang, ok := Languages[name]
	if !ok {
		return Language{}, fmt.Errorf("지원하지 않는 언어입니다: %s", name)
	}
	return lang, nil
}
