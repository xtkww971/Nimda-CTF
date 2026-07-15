// Command worker is the Nimda judge server's consumer process: it reads
// submissions from the Redis Stream that backend-spring produces to and
// dispatches them to a judge.Handler. See docs/redis-stream-queue-design.md
// for the reasoning behind the queue design and failure handling.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"nimda-judge-worker/internal/config"
	"nimda-judge-worker/internal/judge"
	"nimda-judge-worker/internal/queue"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	logger.Info("starting judge worker",
		"consumerName", cfg.ConsumerName,
		"streamKey", cfg.StreamKey,
		"consumerGroup", cfg.ConsumerGroup,
	)

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