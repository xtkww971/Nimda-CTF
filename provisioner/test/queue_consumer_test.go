package test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"nimda-judge-provisioner/internal/config"
	"nimda-judge-provisioner/internal/queue"
)

// 컨슈머 배선만 보는 테스트라 진짜 핸들러가 필요 없다. 받은 요청을 READY 결과로 되돌린다.
//
// 예전에는 handler.StubHandler를 썼다. 메시지 구조가 통합되면서 S3 없이 굴러가는 폴백이
// 의미를 잃어 지웠고(조율자는 이제 S3 없이는 아무 요청도 처리할 수 없다), 여기서만
// 필요한 가짜라 테스트 쪽에 둔다.
type echoHandler struct{}

func (echoHandler) Handle(_ context.Context, msg *queue.Request) (*queue.Result, error) {
	res := &queue.Result{
		UUID:   msg.UUID,
		Status: queue.StatusReady,
		Host:   "chal.nimda.kr",
		Port:   32768,
	}
	if msg.IsolationType == "PER_USER" {
		res.ExpiresAt = "2026-09-10T12:00:00Z"
	}
	return res, nil
}

// redisAddr is the docker test Redis. Start it with:
//
//	docker run -d --name ctf-redis-test -p 6380:6379 redis:7-alpine
const redisAddr = "localhost:6380"

// TestReceiveRequestAndPublishResult drives the whole receive -> respond path
// against a real Redis: publish a create request, let the consumer pick it up
// with a stub handler, and assert the result lands on the result stream.
func TestReceiveRequestAndPublishResult(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	pingCtx, cancelPing := context.WithTimeout(context.Background(), time.Second)
	defer cancelPing()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		t.Skipf("test Redis at %s not reachable (%v); start it with docker to run this test", redisAddr, err)
	}

	// Unique stream/group names so parallel runs and leftovers never collide.
	suffix := time.Now().UnixNano()
	reqStream := "test:ctf:request:ops:" + itoa(suffix)
	resultStream := "test:ctf:request:results:" + itoa(suffix)
	group := "test-provisioners-" + itoa(suffix)

	t.Setenv("REDIS_ADDR", redisAddr)
	t.Setenv("CTF_QUEUE_STREAM_KEY", reqStream)
	t.Setenv("CTF_QUEUE_RESULT_STREAM_KEY", resultStream)
	t.Setenv("CTF_QUEUE_CONSUMER_GROUP", group)
	t.Setenv("CTF_QUEUE_BLOCK_TIMEOUT", "200ms")
	t.Setenv("CONSUMER_NAME", "consumer-under-test")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		rdb.Del(bg, reqStream, resultStream)
		_ = rdb.Close()
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	consumer := queue.NewConsumer(rdb, cfg, echoHandler{}, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := consumer.EnsureGroup(ctx); err != nil {
		t.Fatalf("ensure group: %v", err)
	}

	go consumer.Run(ctx)

	// Publish a PER_USER create request (so we can also check the expiry path).
	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: reqStream,
		Values: map[string]interface{}{
			"uuid":              "uuid-int-1",
			"challengeCode":     "web-01",
			"challengeCategory": "WEB",
			"isolationType":     "PER_USER",
			"flagType":          "DYNAMIC",
		},
	}).Err(); err != nil {
		t.Fatalf("publish request: %v", err)
	}

	// Poll the result stream until the consumer publishes (or we time out).
	values := waitForResult(t, rdb, resultStream, 5*time.Second)

	if values["uuid"] != "uuid-int-1" {
		t.Errorf("uuid = %v, want uuid-int-1", values["uuid"])
	}
	if values["status"] != queue.StatusReady {
		t.Errorf("status = %v, want READY", values["status"])
	}
	// 결과에 남는 식별자는 uuid 하나다. 사용자·문제를 되짚는 건 웹 쪽 몫이다.
	if values["userId"] != nil || values["challengeCode"] != nil {
		t.Errorf("결과에 userId/challengeCode가 실림: %+v", values)
	}
	if values["host"] == "" || values["port"] == nil {
		t.Errorf("READY result missing host/port: %+v", values)
	}
	if values["expiresAt"] == nil {
		t.Errorf("PER_USER result should carry expiresAt: %+v", values)
	}

	// The request should be ACKed (no longer pending).
	pending, err := rdb.XPending(ctx, reqStream, group).Result()
	if err != nil {
		t.Fatalf("XPENDING: %v", err)
	}
	if pending.Count != 0 {
		t.Errorf("request should be ACKed, but %d entries still pending", pending.Count)
	}

	// 그리고 스트림에서도 지워져 있어야 한다.
	//
	// XACK만으로는 엔트리가 남는다. Redis Stream에는 자동 만료가 없어서, 안 지우면
	// 요청을 받는 만큼 무한정 자란다 — pending은 0인데 XLEN만 계속 늘어나는 모양이 된다.
	assertStreamDrained(t, rdb, reqStream, 5*time.Second)
}

// 처리가 끝난 엔트리가 스트림에서 사라질 때까지 기다린다.
// ACK과 XDEL 사이에 아주 짧은 틈이 있어서 바로 보면 아직 남아 있을 수 있다.
func assertStreamDrained(t *testing.T, rdb *redis.Client, stream string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last int64
	for time.Now().Before(deadline) {
		n, err := rdb.XLen(context.Background(), stream).Result()
		if err != nil {
			t.Fatalf("XLEN: %v", err)
		}
		if n == 0 {
			return
		}
		last = n
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("처리가 끝났는데 %s에 엔트리가 %d개 남아 있습니다 (XACK만 하고 XDEL을 안 한 상태)", stream, last)
}

// TestPoisonRequestGoesToDeadLetter verifies a malformed request is not
// retried forever: it lands on the dead-letter stream and gets ACKed.
func TestPoisonRequestGoesToDeadLetter(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	pingCtx, cancelPing := context.WithTimeout(context.Background(), time.Second)
	defer cancelPing()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		t.Skipf("test Redis at %s not reachable (%v)", redisAddr, err)
	}

	suffix := time.Now().UnixNano()
	reqStream := "test:ctf:request:ops:" + itoa(suffix)
	deadLetter := reqStream + ":dead-letter"
	group := "test-provisioners-" + itoa(suffix)

	t.Setenv("REDIS_ADDR", redisAddr)
	t.Setenv("CTF_QUEUE_STREAM_KEY", reqStream)
	t.Setenv("CTF_QUEUE_DEAD_LETTER_STREAM_KEY", deadLetter)
	t.Setenv("CTF_QUEUE_CONSUMER_GROUP", group)
	t.Setenv("CTF_QUEUE_BLOCK_TIMEOUT", "200ms")
	t.Setenv("CONSUMER_NAME", "consumer-under-test")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		rdb.Del(bg, reqStream, deadLetter)
		_ = rdb.Close()
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	consumer := queue.NewConsumer(rdb, cfg, echoHandler{}, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := consumer.EnsureGroup(ctx); err != nil {
		t.Fatalf("ensure group: %v", err)
	}
	go consumer.Run(ctx)

	// challengeCategory가 없다 -> 무엇을 해야 할지 알 수 없다 -> poison.
	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: reqStream,
		Values: map[string]interface{}{"uuid": "uuid-bad", "challengeCode": "web-01"},
	}).Err(); err != nil {
		t.Fatalf("publish poison: %v", err)
	}

	values := waitForResult(t, rdb, deadLetter, 5*time.Second)
	if values["uuid"] != "uuid-bad" {
		t.Errorf("dead-letter entry should preserve original fields: %+v", values)
	}
	if values["failureReason"] == nil {
		t.Errorf("dead-letter entry should record a failureReason: %+v", values)
	}

	// 독성 메시지도 원본 스트림에서는 지워져야 한다. 내용은 데드레터에 남아 있다.
	assertStreamDrained(t, rdb, reqStream, 5*time.Second)
}

func waitForResult(t *testing.T, rdb *redis.Client, stream string, timeout time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msgs, err := rdb.XRange(context.Background(), stream, "-", "+").Result()
		if err == nil && len(msgs) > 0 {
			return msgs[0].Values
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for an entry on %s", timeout, stream)
	return nil
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}
