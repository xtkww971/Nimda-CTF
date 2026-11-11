package heartbeat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type fakeAgent struct {
	exists int64
	err    error
}

func (f *fakeAgent) Exists(ctx context.Context, keys ...string) *redis.IntCmd {
	return redis.NewIntResult(f.exists, f.err)
}

type setCall struct {
	key   string
	value interface{}
	ttl   time.Duration
}

type fakeWeb struct {
	mu     sync.Mutex // Run 테스트에서 고루틴이 쓰고 테스트가 읽는다
	sets   []setCall
	dels   []string
	setErr error
}

func (f *fakeWeb) setCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sets)
}

func (f *fakeWeb) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) *redis.StatusCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, setCall{key, value, ttl})
	if f.setErr != nil {
		return redis.NewStatusResult("", f.setErr)
	}
	return redis.NewStatusResult("OK", nil)
}

func (f *fakeWeb) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dels = append(f.dels, keys...)
	return redis.NewIntResult(1, nil)
}

var opts = Options{
	Key:      "ctf:server:heartbeat",
	Interval: 10 * time.Second,
	TTL:      30 * time.Second,
	AgentKey: "monitor:snapshot",
}

func newBeater(web *fakeWeb, agent *fakeAgent) *Beater {
	b := New(web, agent, opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.now = func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.FixedZone("KST", 9*3600)) }
	return b
}

func TestBeat_AgentAlive_SetsKeyWithTTL(t *testing.T) {
	web := &fakeWeb{}
	b := newBeater(web, &fakeAgent{exists: 1})

	if err := b.Beat(context.Background()); err != nil {
		t.Fatalf("Beat() error = %v", err)
	}
	if len(web.sets) != 1 || len(web.dels) != 0 {
		t.Fatalf("sets=%d dels=%d, want 1 set and no del", len(web.sets), len(web.dels))
	}
	got := web.sets[0]
	if got.key != opts.Key || got.ttl != opts.TTL {
		t.Errorf("SET %q ttl=%s, want %q ttl=%s", got.key, got.ttl, opts.Key, opts.TTL)
	}
	// 값은 UTC로 남긴다. 웹은 해석하지 않지만 서버마다 시간대가 달라도 읽기 쉽게.
	if got.value != "2026-11-11T03:00:00Z" {
		t.Errorf("value = %v, want 2026-11-11T03:00:00Z", got.value)
	}
}

func TestBeat_AgentMissing_DeletesKey(t *testing.T) {
	web := &fakeWeb{}
	b := newBeater(web, &fakeAgent{exists: 0})

	if err := b.Beat(context.Background()); err != nil {
		t.Fatalf("Beat() error = %v", err)
	}
	if len(web.sets) != 0 {
		t.Errorf("agent가 없는데 heartbeat를 썼다")
	}
	if len(web.dels) != 1 || web.dels[0] != opts.Key {
		t.Errorf("dels = %v, want [%s]", web.dels, opts.Key)
	}
}

func TestBeat_AgentRedisDown_DeletesKeyAndReportsError(t *testing.T) {
	web := &fakeWeb{}
	b := newBeater(web, &fakeAgent{err: errors.New("dial tcp: connection refused")})

	if err := b.Beat(context.Background()); err == nil {
		t.Fatal("ctf-redis 조회 실패를 삼켰다")
	}
	if len(web.sets) != 0 || len(web.dels) != 1 {
		t.Errorf("sets=%d dels=%d, want no set and 1 del", len(web.sets), len(web.dels))
	}
}

func TestBeat_WebSetFails_ReturnsError(t *testing.T) {
	web := &fakeWeb{setErr: errors.New("NOAUTH")}
	b := newBeater(web, &fakeAgent{exists: 1})

	if err := b.Beat(context.Background()); err == nil {
		t.Fatal("웹 redis 쓰기 실패를 삼켰다")
	}
}

func TestRun_StopsOnCancelWithoutDeleting(t *testing.T) {
	web := &fakeWeb{}
	b := newBeater(web, &fakeAgent{exists: 1})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()

	// 시작하자마자 한 번은 쓴다. 그 뒤 취소.
	deadline := time.After(time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("Run이 시작 직후 heartbeat를 쓰지 않았다")
		default:
		}
		if web.setCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run이 취소 뒤에 멈추지 않았다")
	}
	// 재시작 동안 웹 버튼이 꺼지지 않게, 종료할 때 지우지 않는다(ADR-0022).
	if len(web.dels) != 0 {
		t.Errorf("종료하면서 heartbeat를 지웠다: %v", web.dels)
	}
}
