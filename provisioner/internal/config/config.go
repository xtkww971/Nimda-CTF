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

	// redis stream 설정 - 웹사이트 설정과 동일하게 설정해야 함
	// todo deadLetterStreamKey, resultStreamKey는 아직 없음
	streamKey           string
	consumerGroup       string
	consumerName        string
	deadLetterStreamKey string
	resultStreamKey     string

	// redis stream에서 읽어올 데이터 설정
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

	// 컨테이너 생성 모듈과 통신하는 주소
	ExecutorSocketPath string

	// 컨테이너 생성 데이터를 저장할 임시 폴더 우치ㅣ
	BuildWorkDir string

	// Agent(컨테이너 생성 프로세스) 소켓 주소. 운영은 unix 소켓, 테스트는 tcp.
	AgentNetwork string // "unix" | "tcp"
	AgentAddr    string

	// StatusAddr가 비어있지 않으면 그 주소에 HTTP 서버를 띄워, 지금 떠 있는 인스턴스와
	// 포트를 실시간으로 조회할 수 있게 한다 (GET /instances). 예: "localhost:6070".
	StatusAddr string
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

		BuildWorkDir: getEnv("CTF_BUILD_WORK_DIR", "/var/lib/ctf/build"),

		AgentNetwork: getEnv("CTF_AGENT_NETWORK", "unix"),
		AgentAddr:    getEnv("CTF_AGENT_ADDR", "/var/run/ctf/agent.sock"),

		StatusAddr: getEnv("CTF_STATUS_ADDR", ""),
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
