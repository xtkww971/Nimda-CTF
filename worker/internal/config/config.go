// Package config loads the worker's runtime configuration from environment
// variables. Defaults mirror the producer side (backend-spring judge.queue.*)
// so both sides agree on stream/group names without extra coordination.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	// Redis connection
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// Consumer group topology - must match the producer (nimda-cite backend).
	// Unexported and only reachable through the getters below so every
	// read goes through one place - see the getter docs for what each
	// value means and why the producer must agree on it.
	streamKey           string
	consumerGroup       string
	consumerName        string
	deadLetterStreamKey string

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
}

// getter
func (c Config) StreamKey() string           { return c.streamKey }
func (c Config) ConsumerGroup() string       { return c.consumerGroup }
func (c Config) ConsumerName() string        { return c.consumerName }
func (c Config) DeadLetterStreamKey() string { return c.deadLetterStreamKey }

func Load() (Config, error) {
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

		streamKey:           getEnv("JUDGE_QUEUE_STREAM_KEY", "judge:submissions"),
		consumerGroup:       getEnv("JUDGE_QUEUE_CONSUMER_GROUP", "judge-workers"),
		consumerName:        consumerName,
		deadLetterStreamKey: getEnv("JUDGE_QUEUE_DEAD_LETTER_STREAM_KEY", "judge:submissions:dead-letter"),

		BatchSize:    getEnvInt64("JUDGE_QUEUE_BATCH_SIZE", 10),
		BlockTimeout: getEnvDuration("JUDGE_QUEUE_BLOCK_TIMEOUT", 5*time.Second),

		ClaimInterval: getEnvDuration("JUDGE_QUEUE_CLAIM_INTERVAL", 30*time.Second),
		MinIdleTime:   getEnvDuration("JUDGE_QUEUE_MIN_IDLE_TIME", 60*time.Second),
		MaxDeliveries: getEnvInt64("JUDGE_QUEUE_MAX_DELIVERIES", 5),

		ReconnectBaseDelay: getEnvDuration("JUDGE_QUEUE_RECONNECT_BASE_DELAY", 500*time.Millisecond),
		ReconnectMaxDelay:  getEnvDuration("JUDGE_QUEUE_RECONNECT_MAX_DELAY", 30*time.Second),

		PprofAddr: getEnv("PPROF_ADDR", ""),
	}

	if cfg.MaxDeliveries < 1 {
		return Config{}, fmt.Errorf("JUDGE_QUEUE_MAX_DELIVERIES must be >= 1, got %d", cfg.MaxDeliveries)
	}

	return cfg, nil
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
