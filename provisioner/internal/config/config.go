// Package config loads the worker's runtime configuration from environment
// variables. Defaults mirror the producer side (backend-spring judge.queue.*)
// so both sides agree on stream/group names without extra coordination.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	// 테스트 환경에서 .env 읽어오기 위해 사용
	"github.com/joho/godotenv"
)

type Config struct {
	// Redis connection
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// AWS connection
	AWSRegion          string
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	S3BucketName       string

	// Consumer group topology - must match the producer (nimda-cite backend).
	// Unexported and only reachable through the getters below so every
	// read goes through one place - see the getter docs for what each
	// value means and why the producer must agree on it.
	streamKey           string
	consumerGroup       string
	consumerName        string
	deadLetterStreamKey string
	resultStreamKey     string

	// Read loop tuning
	BatchSize    int64
	BlockTimeout time.Duration

	// Pending-entry reclaim (crash / stuck-consumer recovery)
	ClaimInterval time.Duration
	MinIdleTime   time.Duration
	MaxDeliveries int64

	// Reconnect backoff for transient Redis/network failures
	ReconnectBaseDelay time.Duration
	ReconnectMaxDelay  time.Duration

	// PprofAddr, if non-empty, starts a net/http/pprof debug server on this
	// address (e.g. "localhost:6060"). Empty (the default) disables it -
	// pprof exposes call stacks/memory contents, so it must never be bound
	// to a non-loopback address outside a developer's own machine.
	PprofAddr string

	// ExecutorSocketPath is the unix socket the Executor process (ADR-0005)
	// listens on. Orchestrator dials this per submission to run a
	// Start/Run/End session instead of shelling out to isolate itself.
	ExecutorSocketPath string
}

// getter
func (c Config) StreamKey() string           { return c.streamKey }
func (c Config) ConsumerGroup() string       { return c.consumerGroup }
func (c Config) ConsumerName() string        { return c.consumerName }
func (c Config) DeadLetterStreamKey() string { return c.deadLetterStreamKey }
func (c Config) ResultStreamKey() string     { return c.resultStreamKey }

func Load() (Config, error) {
	// 로컳 환경에서 .env 폴더 탐색(테스트 환경에서만 사용함)
	loadDotEnv(".env", "../.env", "../../.env", "../../../.env")
	consumerName := getEnv("CONSUMER_NAME", "")
	if consumerName == "" {
		host, err := os.Hostname()
		if err != nil {
			host = "worker"
		}
		consumerName = fmt.Sprintf("%s-%d", host, os.Getpid())
	}

	cfg := Config{
		RedisAddr:     getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		RedisDB:       getEnvInt("REDIS_DB", 0),

		streamKey:           getEnv("CTF_QUEUE_STREAM_KEY", "ctf:instance:ops"),
		consumerGroup:       getEnv("CTF_QUEUE_CONSUMER_GROUP", "ctf-provisioners"),
		consumerName:        consumerName,
		deadLetterStreamKey: getEnv("CTF_QUEUE_DEAD_LETTER_STREAM_KEY", "ctf:instance:ops:dead-letter"),
		resultStreamKey:     getEnv("CTF_QUEUE_RESULT_STREAM_KEY", "ctf:instance:results"),

		AWSRegion:          getEnv("AWS_S3_REGION", "ap-northeast-2"),
		AWSAccessKeyID:     getEnv("AWS_S3_ACCESS_KEY", ""),
		AWSSecretAccessKey: getEnv("AWS_S3_SECRET_KEY", ""),
		S3BucketName:       getEnv("AWS_S3_BUCKET", "nimda-website"),

		BatchSize:    getEnvInt64("CTF_QUEUE_BATCH_SIZE", 10),
		BlockTimeout: getEnvDuration("CTF_QUEUE_BLOCK_TIMEOUT", 5*time.Second),

		ClaimInterval: getEnvDuration("CTF_QUEUE_CLAIM_INTERVAL", 30*time.Second),
		MinIdleTime:   getEnvDuration("CTF_QUEUE_MIN_IDLE_TIME", 60*time.Second),
		MaxDeliveries: getEnvInt64("CTF_QUEUE_MAX_DELIVERIES", 5),

		ReconnectBaseDelay: getEnvDuration("CTF_QUEUE_RECONNECT_BASE_DELAY", 500*time.Millisecond),
		ReconnectMaxDelay:  getEnvDuration("CTF_QUEUE_RECONNECT_MAX_DELAY", 30*time.Second),

		PprofAddr: getEnv("PPROF_ADDR", ""),

		ExecutorSocketPath: getEnv("EXECUTOR_SOCKET_PATH", "/var/run/judge/executor.sock"),
	}

	if cfg.MaxDeliveries < 1 {
		return Config{}, fmt.Errorf("CTF_QUEUE_MAX_DELIVERIES must be >= 1, got %d", cfg.MaxDeliveries)
	}

	return cfg, nil
}

// loadDotEnv loads the first candidate file that exists. godotenv.Load itself
// stops at the first candidate it can't find, so passing multiple fallback
// paths straight to it never reaches the later ones - the test binary's cwd
// is the package dir (e.g. internal/awsUtil), not the module root, so ".env"
// alone would never resolve without this walk-and-pick-first logic.
func loadDotEnv(candidates ...string) {
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			_ = godotenv.Load(path)
			return
		}
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

func getEnvInt64(key string, fallback int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
