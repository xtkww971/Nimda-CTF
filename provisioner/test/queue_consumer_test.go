package test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"nimda-judge-provisioner/internal/config"
	"nimda-judge-provisioner/internal/handler"
	"nimda-judge-provisioner/internal/queue"
)

// redisAddr is the docker test Redis. Start it with:
//
//	docker run -d --name ctf-redis-test -p 6380:6379 redis:7-alpine
const redisAddr = "localhost:6380"

// TestReceiveRequestAndPublishResult drives the whole receive -> respond path
// against a real Redis: publish a create request, let the consumer pick it up
// with the StubHandler, and assert the result lands on the result stream.
func TestReceiveRequestAndPublishResult(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	pingCtx, cancelPing := context.WithTimeout(context.Background(), time.Second)
	defer cancelPing()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		t.Skipf("test Redis at %s not reachable (%v); start it with docker to run this test", redisAddr, err)
	}

	// Unique stream/group names so parallel runs and leftovers never collide.
	suffix := time.Now().UnixNano()
	reqStream := "test:ctf:instance:ops:" + itoa(suffix)
	resultStream := "test:ctf:instance:results:" + itoa(suffix)
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
	consumer := queue.NewConsumer(rdb, cfg, handler.NewStubHandler(logger), logger)

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
			"action":        "CREATE",
			"requestId":     "req-int-1",
			"challengeCode": "web-01",
			"userId":        "42",
			"isolationType": "PER_USER",
			"flagType":      "DYNAMIC",
			"requestedAt":   "2026-08-12T10:00:00",
		},
	}).Err(); err != nil {
		t.Fatalf("publish request: %v", err)
	}

	// Poll the result stream until the consumer publishes (or we time out).
	values := waitForResult(t, rdb, resultStream, 5*time.Second)

	if values["requestId"] != "req-int-1" {
		t.Errorf("requestId = %v, want req-int-1", values["requestId"])
	}
	if values["status"] != queue.StatusReady {
		t.Errorf("status = %v, want READY", values["status"])
	}
	if values["challengeCode"] != "web-01" || values["userId"] != "42" {
		t.Errorf("result did not echo identity: %+v", values)
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
	reqStream := "test:ctf:instance:ops:" + itoa(suffix)
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
	consumer := queue.NewConsumer(rdb, cfg, handler.NewStubHandler(logger), logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := consumer.EnsureGroup(ctx); err != nil {
		t.Fatalf("ensure group: %v", err)
	}
	go consumer.Run(ctx)

	// Missing challengeCode -> unparseable -> poison.
	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: reqStream,
		Values: map[string]interface{}{"action": "CREATE", "requestId": "req-bad"},
	}).Err(); err != nil {
		t.Fatalf("publish poison: %v", err)
	}

	values := waitForResult(t, rdb, deadLetter, 5*time.Second)
	if values["requestId"] != "req-bad" {
		t.Errorf("dead-letter entry should preserve original fields: %+v", values)
	}
	if values["failureReason"] == nil {
		t.Errorf("dead-letter entry should record a failureReason: %+v", values)
	}
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
