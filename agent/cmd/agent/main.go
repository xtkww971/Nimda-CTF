// Command agent is the privileged CTF process: it listens on a unix socket for
// the Provisioner, builds/runs challenge containers via the host container
// runtime, and reports back. It is the only component that touches the runtime
// (ADR-0007 / ADR-0009).
package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"nimda-ctf-agent/internal/agentserver"
	"nimda-ctf-agent/internal/runtime"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// UPS 연결 설정
	socketPath := getenv("AGENT_SOCKET_PATH", "/var/run/ctf/agent.sock")
	host := getenv("CTF_INSTANCE_HOST", "127.0.0.1")

	rt := runtime.NewDocker(runtime.Options{
		Runtime:     os.Getenv("CTF_CONTAINER_RUNTIME"), // "" = default; "runsc" in prod (gVisor)
		MemoryLimit: getenv("CTF_INSTANCE_MEMORY", "256m"),
		PidsLimit:   getenvInt("CTF_INSTANCE_PIDS", 256),
	})
	handler := agentserver.NewDockerHandler(rt, host, logger)

	// A stale socket file from a previous crash would block bind; remove it.
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		logger.Warn("기존 소켓 파일 제거 실패", "path", socketPath, "error", err)
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		logger.Error("소켓 리슨 실패", "path", socketPath, "error", err)
		os.Exit(1)
	}
	defer ln.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("agent ready", "socket", socketPath, "instanceHost", host)
	agentserver.Serve(ctx, ln, handler, logger)
	logger.Info("shutdown signal received, agent stopped")
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
