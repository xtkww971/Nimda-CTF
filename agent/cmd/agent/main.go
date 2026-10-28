// agent는 특권 CTF 프로세스다. 조율자가 넣은 요청을 받아 호스트의 컨테이너 런타임으로
// 문제 컨테이너를 빌드/실행하고 결과를 돌려준다.
// 런타임을 직접 만지는 유일한 조각 (ADR-0007 / ADR-0009).
//
// 통신은 ctf-redis의 Redis Stream이다 (ADR-0012).
// 요청 ctf:agent:ops → 처리 → 응답 ctf:agent:results.
// "어느 요청이 어느 컨테이너가 됐는지"는 같은 redis에 uuid: 접두사로 기록한다.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"nimda-ctf-agent/internal/agentqueue"
	"nimda-ctf-agent/internal/agentserver"
	"nimda-ctf-agent/internal/monitor"
	"nimda-ctf-agent/internal/reaper"
	"nimda-ctf-agent/internal/registry"
	"nimda-ctf-agent/internal/runtime"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// web 참가자가 인스턴스에 접속할 주소.
	host := getenv("CTF_INSTANCE_HOST", "127.0.0.1")
	// 조율자와 공유하는 빌드 폴더 루트. 이 아래 경로만 빌드 컨텍스트로 허용한다.
	buildRoot := getenv("CTF_BUILD_WORK_DIR", "/var/lib/ctf/build")

	// pwn(TCP) 전용 설정.
	pwn := agentserver.PwnOptions{
		Host:    os.Getenv("CTF_PWN_PUBLIC_HOST"), // TCP를 쓸 거면 필수. 참가자에게 보여줄 주소
		BindIP:  getenv("CTF_PWN_BIND_IP", "0.0.0.0"),
		Network: getenv("CTF_PWN_NETWORK", "ctf-pwn"),
	}
	// 포트 풀 범위. 밖에 열 포트는 여기서만 나눠준다(ADR-0013).
	portLo, portHi, err := parsePortRange(getenv("CTF_PWN_PORT_RANGE", "30000-30199"))
	if err != nil {
		logger.Error("CTF_PWN_PORT_RANGE 해석 실패", "error", err)
		os.Exit(1)
	}

	// 도커 데몬 소켓에 붙는다. 여기서 실패하면 이 프로세스가 할 수 있는 일이 없다.
	rt, err := runtime.NewDocker(runtime.Options{
		Runtime:     os.Getenv("CTF_CONTAINER_RUNTIME"), // "" = default; "runsc" in prod (gVisor)
		MemoryLimit: getenv("CTF_INSTANCE_MEMORY", "256m"),
		PidsLimit:   getenvInt("CTF_INSTANCE_PIDS", 256),
		CPULimit:    getenvFloat("CTF_INSTANCE_CPU", 0.5),
		TmpfsSize:   getenv("CTF_INSTANCE_TMPFS_SIZE", "64m"),
	})
	if err != nil {
		logger.Error("도커 데몬에 연결하지 못했습니다", "error", err)
		os.Exit(1)
	}
	defer rt.Close()
	instances := agentserver.NewDockerHandler(rt, host, buildRoot, pwn, logger)

	// 조율자와 공유하는 통. 요청·응답 스트림과 인스턴스 기록이 전부 여기 있다.
	rdb := redis.NewClient(&redis.Options{
		Addr:     getenv("CTF_AGENT_REDIS_ADDR", "ctf-redis:6379"),
		Password: os.Getenv("CTF_AGENT_REDIS_PASSWORD"),
		DB:       getenvInt("CTF_AGENT_REDIS_DB", 0),
	})
	defer rdb.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := waitForRedis(ctx, rdb, logger); err != nil {
		logger.Error("giving up waiting for redis", "error", err)
		os.Exit(1)
	}

	opts := agentqueue.Options{
		Stream:        getenv("CTF_QUEUE_AGENT_STREAM_KEY", "ctf:agent:ops"),
		Group:         getenv("CTF_QUEUE_AGENT_CONSUMER_GROUP", "ctf-agents"),
		Consumer:      consumerName(),
		ResultStream:  getenv("CTF_QUEUE_AGENT_RESULT_STREAM_KEY", "ctf:agent:results"),
		BatchSize:     int64(getenvInt("CTF_QUEUE_BATCH_SIZE", 10)),
		BlockTimeout:  getenvDuration("CTF_QUEUE_BLOCK_TIMEOUT", 5*time.Second),
		ReconnectBase: getenvDuration("CTF_QUEUE_RECONNECT_BASE_DELAY", 500*time.Millisecond),
		ReconnectMax:  getenvDuration("CTF_QUEUE_RECONNECT_MAX_DELAY", 30*time.Second),
	}

	reg := registry.New(rdb)

	// 포트 풀을 다시 계산한다. 요청을 받기 전에, 지금 떠 있는 컨테이너가 쓰는 포트를 빼고 채운다.
	// 반납 못 하고 죽은 포트는 여기서 돌아오고, 이미 쓰는 포트는 다시 안 나간다(이슈 #29).
	used, err := rt.UsedPorts(ctx)
	if err != nil {
		logger.Error("사용 중인 포트 조회 실패", "error", err)
		os.Exit(1)
	}
	if err := reg.InitPortPool(ctx, portLo, portHi, used); err != nil {
		logger.Error("포트 풀 초기화 실패", "error", err)
		os.Exit(1)
	}
	logger.Info("포트 풀 초기화 완료", "range", fmt.Sprintf("%d-%d", portLo, portHi), "used", len(used))

	dispatcher := agentqueue.NewDispatcher(instances, reg, logger)
	consumer := agentqueue.NewConsumer(rdb, opts, dispatcher, logger)
	if err := consumer.EnsureGroup(ctx); err != nil {
		logger.Error("failed to ensure consumer group",
			"stream", opts.Stream, "group", opts.Group, "error", err)
		os.Exit(1)
	}

	logger.Info("agent ready, consuming queue",
		"stream", opts.Stream, "group", opts.Group, "consumer", opts.Consumer,
		"resultStream", opts.ResultStream, "instanceHost", host, "buildRoot", buildRoot)

	// 대시보드가 볼 snapshot을 주기적으로 ctf-redis에 쓴다 (Feature-01).
	// agent는 listener를 열지 않기 때문에 이게 대시보드로 가는 유일한 통로다.
	go monitor.New(rt, reg, rdb, logger).Run(ctx)

	// 만료된 PER_USER 인스턴스와 idle 공유 인스턴스를 주기적으로 내린다 (ADR-0014, ADR-0019).
	reapInterval := getenvDuration("CTF_REAP_INTERVAL", 30*time.Second)
	sharedGrace := getenvDuration("CTF_SHARED_IDLE_GRACE", 10*time.Minute)
	go reaper.New(rt, reg, reapInterval, sharedGrace, logger).Run(ctx)

	consumer.Run(ctx)
	logger.Info("shutdown signal received, agent stopped")
}

// 같은 그룹 안에서 이 프로세스를 구분하는 이름. pending이 누구 것인지 보려면 필요하다.
func consumerName() string {
	if v := os.Getenv("CONSUMER_NAME"); v != "" {
		return v
	}
	host, err := os.Hostname()
	if err != nil {
		host = "agent"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
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
		if delay *= 2; delay > maxDelay {
			delay = maxDelay
		}
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// "30000-30199" 같은 범위를 lo, hi로 푼다. lo > hi거나 형식이 틀리면 에러다.
func parsePortRange(s string) (lo, hi int, err error) {
	loStr, hiStr, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, fmt.Errorf("형식은 \"lo-hi\"여야 합니다: %q", s)
	}
	lo, err = strconv.Atoi(strings.TrimSpace(loStr))
	if err != nil {
		return 0, 0, fmt.Errorf("시작 포트 해석 실패 (%q): %w", loStr, err)
	}
	hi, err = strconv.Atoi(strings.TrimSpace(hiStr))
	if err != nil {
		return 0, 0, fmt.Errorf("끝 포트 해석 실패 (%q): %w", hiStr, err)
	}
	if lo < 1 || hi > 65535 || lo > hi {
		return 0, 0, fmt.Errorf("포트 범위가 잘못됐습니다: %d-%d", lo, hi)
	}
	return lo, hi, nil
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getenvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
