// Command worker is the Nimda judge server's consumer process: it reads
// submissions from the Redis Stream that backend-spring produces to and
// dispatches them to a judge.Handler. See docs/redis-stream-queue-design.md
// for the reasoning behind the queue design and failure handling.
package main

import (
	"context"
	"log/slog"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof/* on http.DefaultServeMux; only served if cfg.PprofAddr is set
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"nimda-judge-worker/internal/config"
	"nimda-judge-worker/internal/judge"
	"nimda-judge-worker/internal/queue"
)

func main() {
	// LOG_LEVEL is read directly (not via config.Load) so the logger exists
	// before configuration is validated and can report a bad config.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(os.Getenv("LOG_LEVEL")),
	}))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	logger.Info("starting judge worker",
		"consumerName", cfg.ConsumerName(),
		"streamKey", cfg.StreamKey(),
		"consumerGroup", cfg.ConsumerGroup(),
	)

	if cfg.PprofAddr != "" {
		go func() {
			logger.Info("starting pprof debug server", "addr", cfg.PprofAddr)
			if err := http.ListenAndServe(cfg.PprofAddr, nil); err != nil {
				logger.Error("pprof server stopped", "error", err)
			}
		}()
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer rdb.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := waitForRedis(ctx, rdb, logger); err != nil {
		logger.Error("giving up waiting for redis", "error", err)
		os.Exit(1)
	}

	consumer := queue.NewConsumer(rdb, cfg, judge.NewStubHandler(logger), logger)
	if err := consumer.EnsureGroup(ctx); err != nil {
		logger.Error("failed to ensure consumer group", "error", err)
		os.Exit(1)
	}

	logger.Info("judge worker ready, consuming submissions")
	consumer.Run(ctx)
	logger.Info("shutdown signal received, worker stopped")
}

// parseLogLevel maps LOG_LEVEL (debug/info/warn/error, case-insensitive) to
// a slog.Level, defaulting to Info on anything unrecognized so a typo
// doesn't silently suppress normal-severity logs.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// waitForRedis retries the initial connection with backoff instead of
// crash-looping the process when Redis isn't up yet (e.g. during a
// docker-compose cold start where dependency ordering isn't guaranteed).
func waitForRedis(ctx context.Context, rdb *redis.Client, logger *slog.Logger) error {
	delay := 500 * time.Millisecond
	const maxDelay = 30 * time.Second

	for {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := rdb.Ping(pingCtx).Err()
		cancel()
		if err == nil {
			return nil
		}

		logger.Warn("redis not reachable yet, retrying", "error", err, "wait", delay.String())
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}