// dashboard는 인스턴스 모니터링 화면이다 (Feature-01).
//
// agent가 ctf-redis에 써둔 snapshot을 읽어서 브라우저에 보여준다.
// ctf-internal 망에만 붙고, 호스트의 127.0.0.1로만 publish한다.
// 서버에서 볼 때는 SSH 터널(ssh -L 6070:127.0.0.1:6070 ...)로 접근한다.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"nimda-ctf-dashboard/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	addr := getenv("CTF_DASHBOARD_ADDR", ":6070")
	// Host 헤더로 허용할 이름. SSH 터널이나 로컬에서 열면 localhost, 127.0.0.1로 들어온다.
	allowed := strings.Split(getenv("CTF_DASHBOARD_ALLOWED_HOSTS", "localhost,127.0.0.1"), ",")

	rdb := redis.NewClient(&redis.Options{
		Addr:     getenv("CTF_DASHBOARD_REDIS_ADDR", "ctf-redis:6379"),
		Password: os.Getenv("CTF_DASHBOARD_REDIS_PASSWORD"),
		DB:       getenvInt("CTF_DASHBOARD_REDIS_DB", 0),
	})
	defer rdb.Close()

	// 대시보드 서버 초기화
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(rdb, allowed, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("dashboard listening", "addr", addr, "allowedHosts", allowed)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("dashboard 서버 실패", "error", err)
		os.Exit(1)
	}
	logger.Info("dashboard stopped")
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
