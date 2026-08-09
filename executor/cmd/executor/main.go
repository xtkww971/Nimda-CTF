// Command executor is the ADR-0005 Executor process: it owns the isolate
// box pool and does nothing else. It listens on a unix socket for
// Start/Run/End sessions from the Orchestrator (worker/) and never touches
// Redis, S3, or verdict logic - see docs/adr/[ADR-0005] for the role split.
package main

import (
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"nimda-judge-executor/internal/execserver"
	"nimda-judge-executor/internal/isolate"
)

func main() {
	socketPath := getEnv("EXECUTOR_SOCKET_PATH", "/var/run/judge/executor.sock")
	metaDir := getEnv("EXECUTOR_META_DIR", "/tmp/judge-meta")
	poolSize := getEnvInt("EXECUTOR_BOX_POOL_SIZE", 4)

	if binPath := os.Getenv("EXECUTOR_ISOLATE_BIN"); binPath != "" {
		isolate.BinaryPath = binPath
	}

	if err := os.MkdirAll(metaDir, 0755); err != nil {
		log.Fatalf("meta 디렉토리 생성 실패: %v", err)
	}

	// 이전 실행이 비정상 종료됐다면 소켓 파일이 남아있을 수 있으므로 미리 정리한다.
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatalf("기존 소켓 파일 정리 실패: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
		log.Fatalf("소켓 디렉토리 생성 실패: %v", err)
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("유닉스 소켓 리슨 실패 (%s): %v", socketPath, err)
	}
	defer ln.Close()

	handler := &execserver.Handler{
		Pool:    isolate.NewBoxPool(poolSize),
		MetaDir: metaDir,
	}

	log.Printf("executor 기동: socket=%s poolSize=%d", socketPath, poolSize)
	if err := execserver.Serve(ln, handler); err != nil {
		log.Fatalf("executor 서버 종료: %v", err)
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
