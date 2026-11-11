package main

import (
	"context"
	"log/slog"
	"net/http"
	_ "net/http/pprof" // /debug/pprof/* 를 등록만 해둠. 실제로는 cfg.PprofAddr가 설정됐을 때만 서비스됨
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	protocol "nimda-ctf-protocol"
	"nimda-judge-provisioner/internal/agentclient"
	"nimda-judge-provisioner/internal/aws"
	"nimda-judge-provisioner/internal/config"
	"nimda-judge-provisioner/internal/handler"
	"nimda-judge-provisioner/internal/heartbeat"
	"nimda-judge-provisioner/internal/queue"
)

func main() {
	// logger 생성
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(os.Getenv("LOG_LEVEL")),
	}))
	// 환경 설정 불러오기
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	logger.Info("starting provisioner",
		"consumerName", cfg.ConsumerName(),
		"streamKey", cfg.StreamKey(),
		"resultStreamKey", cfg.ResultStreamKey(),
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

	// 웹사이트 통신 파이프라인
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer rdb.Close()

	// agent 통신 파이프라인 설정 값 주입
	agentRdb := rdb
	if cfg.AgentRedisAddr != "" {
		addr, password, db := cfg.AgentRedis()
		agentRdb = redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
		defer agentRdb.Close()
	}

	// 종료 시 사용할 stop 함수 받아오기 및 최상위 ctx 설정
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// redis 연결 대기
	if err := waitForRedis(ctx, rdb, logger); err != nil {
		logger.Error("giving up waiting for redis", "error", err)
		os.Exit(1)
	}

	if agentRdb != rdb {
		if err := waitForRedis(ctx, agentRdb, logger); err != nil {
			logger.Error("giving up waiting for agent redis", "addr", cfg.AgentRedisAddr, "error", err)
			os.Exit(1)
		}
	}

	// s3 연결
	s3, err := aws.NewS3Storage(ctx, cfg)
	if err != nil {
		logger.Error("S3 연결 실패, Provisioner를 시작할 수 없습니다", "error", err)
		os.Exit(1)
	}

	// agent 통신 연결
	dispatcher := agentclient.NewStreamDispatcher(agentRdb, cfg.AgentStreamKey(), logger)

	// 웹 요청 컨슈머
	router := handler.NewRouter(
		handler.NewTaskDispatcher(
			handler.NewHandler(s3, dispatcher, cfg, logger),
			handler.NewDownloadHandler(s3, cfg, logger),
			logger,
		),
		logger,
	)
	consumer := queue.NewConsumer(rdb, cfg, router, logger)
	if err := consumer.EnsureGroup(ctx); err != nil {
		logger.Error("failed to ensure consumer group", "error", err)
		os.Exit(1)
	}

	// agent result consumer
	agentResults := queue.NewAgentResultConsumer(agentRdb, rdb, cfg, handler.NewAgentResultMapper(logger), logger)
	if err := agentResults.EnsureGroup(ctx); err != nil {
		logger.Error("failed to ensure agent result consumer group", "error", err)
		os.Exit(1)
	}
	logger.Info("agent result consumer ready",
		"stream", cfg.AgentResultStreamKey(), "group", cfg.AgentConsumerGroup(),
		"resultStream", cfg.ResultStreamKey())
	go agentResults.Run(ctx)

	// 웹에 "요청을 받을 수 있다"는 신호를 남긴다. agent snapshot이 있을 때만 쓴다 (ADR-0022).
	go heartbeat.New(rdb, agentRdb, heartbeat.Options{
		Key:      cfg.HeartbeatKey,
		Interval: cfg.HeartbeatInterval,
		TTL:      cfg.HeartbeatTTL,
		AgentKey: protocol.MonitorSnapshotKey,
	}, logger).Run(ctx)

	logger.Info("provisioner ready, consuming queue")
	consumer.Run(ctx)
	logger.Info("shutdown signal received, provisioner stopped")
}

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
