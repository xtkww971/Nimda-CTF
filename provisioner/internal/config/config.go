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
	// 웹 백엔드와 공유하는 Redis. 인스턴스 생성 요청과 결과가 여기로 오간다.
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// 에이전트와만 쓰는 Redis(ctf-redis). 웹과 섞지 않으려고 통을 나눠 뒀다.
	// 비어 있으면 위의 Redis를 그대로 쓴다 — 로컬에서 통 하나로 돌릴 때를 위한 폴백.
	AgentRedisAddr     string
	AgentRedisPassword string
	AgentRedisDB       int

	// AWS connection
	AWSRegion          string
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	S3BucketName       string

	// 웹 백엔드 ↔ 조율자 스트림. 웹사이트 설정과 동일해야 함.
	//
	// 예전에는 인스턴스용과 첨부파일 다운로드용으로 한 쌍씩 총 네 개를 팠다. 요청 메시지가
	// 하나로 합쳐지면서(이슈 #148) 요청 하나, 결과 하나로 줄었다.
	streamKey           string
	consumerGroup       string
	consumerName        string
	deadLetterStreamKey string
	resultStreamKey     string

	// 조율자 ↔ 에이전트 전용 스트림. 웹이 보는 스트림과 섞지 않는다 — 웹은 "인스턴스 하나
	// 달라"까지만 알면 되고, 그걸 빌드하느라 오간 중간 메시지는 알 필요가 없다.
	// 조율자가 agentStreamKey로 요청을 넣고, 에이전트가 agentResultStreamKey로 결과를 돌려준다.
	agentStreamKey           string
	agentConsumerGroup       string
	agentResultStreamKey     string
	agentDeadLetterStreamKey string

	// redis stream에서 읽어올 데이터 설정
	BatchSize    int64
	BlockTimeout time.Duration

	// 처리 중 멈춘(크래시/먹통) 컨슈머의 pending 메시지 회수용
	ClaimInterval time.Duration
	MinIdleTime   time.Duration
	MaxDeliveries int64

	// Redis/네트워크가 일시적으로 삐끗할 때 재연결 백오프
	ReconnectBaseDelay time.Duration
	ReconnectMaxDelay  time.Duration

	// PprofAddr가 비어있지 않으면 그 주소(예: "localhost:6060")에 pprof 디버그 서버를 띄운다.
	// 기본은 빈 값(꺼짐). pprof는 콜스택·메모리 내용을 노출하므로, 개발자 자기 머신 밖의
	// loopback 아닌 주소에는 절대 걸면 안 된다.
	PprofAddr string

	// 컨테이너 생성 데이터를 저장할 임시 폴더 위치.
	// 조율자가 여기에 풀고 에이전트가 같은 경로에서 빌드하므로, 양쪽에 같은 경로로
	// 붙어 있어야 한다(compose의 ctf-build 볼륨).
	BuildWorkDir string

	// presigned URL 유효 시간. 링크가 새어나가도 금방 죽도록 짧게 잡는다.
	PresignTTL time.Duration

	// 웹 redis에 쓰는 가용성 신호 (ADR-0022). 키는 웹 설정 ctf.queue.heartbeat-key와 같아야 한다.
	// 한쪽만 바뀌면 웹은 키를 영영 못 찾아서 인스턴스 버튼이 계속 꺼져 있다.
	HeartbeatKey      string
	HeartbeatInterval time.Duration
	HeartbeatTTL      time.Duration
}

// getter
func (c Config) StreamKey() string           { return c.streamKey }
func (c Config) ConsumerGroup() string       { return c.consumerGroup }
func (c Config) ConsumerName() string        { return c.consumerName }
func (c Config) DeadLetterStreamKey() string { return c.deadLetterStreamKey }
func (c Config) ResultStreamKey() string     { return c.resultStreamKey }

func (c Config) AgentStreamKey() string           { return c.agentStreamKey }
func (c Config) AgentConsumerGroup() string       { return c.agentConsumerGroup }
func (c Config) AgentResultStreamKey() string     { return c.agentResultStreamKey }
func (c Config) AgentDeadLetterStreamKey() string { return c.agentDeadLetterStreamKey }

// 에이전트와 쓸 Redis 접속 정보. 따로 지정하지 않았으면 웹과 같은 통을 쓴다.
//
// 운영·compose에서는 ctf-redis를 따로 띄우지만, 로컬에서 통 하나로 디버깅할 때
// CTF_AGENT_REDIS_ADDR만 비우면 별도 설정 없이 굴러가게 하려는 폴백이다.
func (c Config) AgentRedis() (addr, password string, db int) {
	if c.AgentRedisAddr == "" {
		return c.RedisAddr, c.RedisPassword, c.RedisDB
	}
	return c.AgentRedisAddr, c.AgentRedisPassword, c.AgentRedisDB
}

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

		AgentRedisAddr:     getEnv("CTF_AGENT_REDIS_ADDR", ""),
		AgentRedisPassword: getEnv("CTF_AGENT_REDIS_PASSWORD", ""),
		AgentRedisDB:       getEnvInt("CTF_AGENT_REDIS_DB", 0),

		// 기본값은 웹 백엔드(application.yml)와 같은 값으로 맞춰 둔다. 한쪽만 바뀌면
		// 요청이 아무도 안 읽는 스트림에 쌓이고, 그건 조용히 실패한다.
		streamKey:           getEnv("CTF_QUEUE_STREAM_KEY", "ctf:request:ops"),
		consumerGroup:       getEnv("CTF_QUEUE_CONSUMER_GROUP", "ctf-provisioners"),
		consumerName:        consumerName,
		resultStreamKey:     getEnv("CTF_QUEUE_RESULT_STREAM_KEY", "ctf:request:results"),
		deadLetterStreamKey: getEnv("CTF_QUEUE_DEAD_LETTER_STREAM_KEY", "ctf:request:dead-letter"),

		// 웹 백엔드와 맞출 필요가 없는 내부 스트림이라 기본값을 준다. 웹이 보는 스트림 키와
		// 달리 양쪽(조율자·에이전트)이 우리 코드라, 합의만 되면 된다.
		agentStreamKey:       getEnv("CTF_QUEUE_AGENT_STREAM_KEY", "ctf:agent:ops"),
		agentConsumerGroup:   getEnv("CTF_QUEUE_AGENT_CONSUMER_GROUP", "ctf-agents"),
		agentResultStreamKey: getEnv("CTF_QUEUE_AGENT_RESULT_STREAM_KEY", "ctf:agent:results"),
		// 빈 값으로 두면 데드레터 XADD가 이름 없는 스트림으로 나간다. 웹과 맞출 필요가 없는
		// 내부 스트림이라 여기서 기본값을 정한다.
		agentDeadLetterStreamKey: getEnv("CTF_QUEUE_AGENT_DEAD_LETTER_STREAM_KEY", "ctf:agent:dead-letter"),

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

		BuildWorkDir: getEnv("CTF_BUILD_WORK_DIR", "/var/lib/ctf/build"),

		// 웹 백엔드가 발급된 링크를 10분 캐시해 재사용하므로, 서명 수명은 그보다 넉넉해야
		// 한다. 짧게 잡으면 캐시에서 꺼낸 링크가 이미 죽어 있을 수 있다.
		PresignTTL: getEnvDuration("CTF_PRESIGN_TTL", 15*time.Minute),

		HeartbeatKey:      getEnv("CTF_HEARTBEAT_KEY", "ctf:server:heartbeat"),
		HeartbeatInterval: getEnvDuration("CTF_HEARTBEAT_INTERVAL", 10*time.Second),
		HeartbeatTTL:      getEnvDuration("CTF_HEARTBEAT_TTL", 30*time.Second),
	}

	if cfg.MaxDeliveries < 1 {
		return Config{}, fmt.Errorf("CTF_QUEUE_MAX_DELIVERIES must be >= 1, got %d", cfg.MaxDeliveries)
	}

	// presigned URL은 "잠깐만 유효한 링크"가 존재 이유다. 너무 길게 잡으면 링크 하나가
	// 사실상 영구 공개 URL이 되므로 상한을 둔다. (S3 SigV4 자체 상한은 7일)
	if cfg.PresignTTL <= 0 || cfg.PresignTTL > time.Hour {
		return Config{}, fmt.Errorf("CTF_PRESIGN_TTL must be in (0, 1h], got %s", cfg.PresignTTL)
	}

	// TTL이 주기보다 짧으면 쓰는 사이사이에 키가 비어서 웹 버튼이 깜빡인다.
	if cfg.HeartbeatInterval <= 0 || cfg.HeartbeatTTL <= cfg.HeartbeatInterval {
		return Config{}, fmt.Errorf("CTF_HEARTBEAT_TTL(%s) must be greater than CTF_HEARTBEAT_INTERVAL(%s) > 0",
			cfg.HeartbeatTTL, cfg.HeartbeatInterval)
	}

	return cfg, nil
}

// 후보 중 처음으로 존재하는 .env 파일을 읽는다. godotenv.Load는 못 찾으면 거기서 멈춰서
// 여러 후보를 한 번에 넘기면 뒤쪽 경로까진 안 간다. 테스트 바이너리의 cwd는 모듈 루트가
// 아니라 패키지 폴더(예: internal/aws)라, 이렇게 훑어서 먼저 찾은 걸 쓰지 않으면
// ".env" 하나로는 못 찾는다.
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
