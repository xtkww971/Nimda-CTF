// Package heartbeat는 웹 redis에 "CTF 서버가 인스턴스 요청을 받을 수 있다"는 신호를 남긴다 (ADR-0022).
//
// 웹은 스트림에 요청을 넣기만 하고 누가 읽는지 모른다. 그래서 provisioner가 주기적으로 TTL 걸린
// 키를 쓰고, 웹은 그 키가 있는지만 본다. provisioner만 살아 있어도 agent가 죽었으면 컨테이너를
// 못 만들므로, agent가 ctf-redis에 쓰는 monitor snapshot이 있을 때만 쓴다.
package heartbeat

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// 웹 redis 쪽에 필요한 것만. *redis.Client가 그대로 맞는다.
type WebStore interface {
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// ctf-redis 쪽에 필요한 것만.
type AgentStore interface {
	Exists(ctx context.Context, keys ...string) *redis.IntCmd
}

type Options struct {
	Key      string        // 웹 redis에 쓸 키 (웹 설정 ctf.queue.heartbeat-key와 같아야 한다)
	Interval time.Duration // 쓰는 주기
	TTL      time.Duration // 키 수명. Interval보다 길어야 한 번 늦어도 안 꺼진다
	AgentKey string        // agent가 살아 있으면 ctf-redis에 있는 키 (protocol.MonitorSnapshotKey)
}

type Beater struct {
	web    WebStore
	agent  AgentStore
	opts   Options
	logger *slog.Logger
	now    func() time.Time

	// 마지막 판정. 바뀔 때만 로그를 남긴다(10초마다 같은 로그가 쌓이지 않게).
	known bool
	alive bool
}

func New(web WebStore, agent AgentStore, opts Options, logger *slog.Logger) *Beater {
	return &Beater{web: web, agent: agent, opts: opts, logger: logger, now: time.Now}
}

// ctx가 끝날 때까지 Interval마다 Beat한다. 시작하자마자 한 번 한다.
//
// 끝날 때 키를 지우지 않는다. 배포 재시작 몇 초 동안 웹에서 버튼이 사라지면 안 되고,
// 그 사이 요청은 스트림에 남아 다시 뜬 뒤 처리된다. 정말 죽었으면 TTL이 지나 사라진다.
func (b *Beater) Run(ctx context.Context) {
	t := time.NewTicker(b.opts.Interval)
	defer t.Stop()
	for {
		_ = b.Beat(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// 한 번 판정해서 쓰거나 지운다. 테스트에서 직접 부른다.
func (b *Beater) Beat(ctx context.Context) error {
	n, err := b.agent.Exists(ctx, b.opts.AgentKey).Result()
	if err != nil {
		// ctf-redis에 못 붙으면 agent에 일을 넘길 수도 없다.
		return errors.Join(err, b.down(ctx, "agent redis 조회 실패", err))
	}
	if n == 0 {
		return b.down(ctx, "agent snapshot 없음", nil)
	}

	value := b.now().UTC().Format(time.RFC3339)
	if err := b.web.Set(ctx, b.opts.Key, value, b.opts.TTL).Err(); err != nil {
		b.logger.Warn("heartbeat 쓰기 실패", "key", b.opts.Key, "error", err)
		return err
	}
	b.mark(true, "agent 응답 확인", nil)
	return nil
}

func (b *Beater) down(ctx context.Context, reason string, cause error) error {
	if err := b.web.Del(ctx, b.opts.Key).Err(); err != nil {
		b.logger.Warn("heartbeat 지우기 실패", "key", b.opts.Key, "error", err)
		return err
	}
	b.mark(false, reason, cause)
	return nil
}

func (b *Beater) mark(alive bool, reason string, cause error) {
	if b.known && b.alive == alive {
		return
	}
	b.known, b.alive = true, alive
	if alive {
		b.logger.Info("heartbeat 켜짐: 웹에 사용 가능으로 알림", "key", b.opts.Key, "reason", reason)
		return
	}
	b.logger.Warn("heartbeat 꺼짐: 웹에 사용 불가로 알림", "key", b.opts.Key, "reason", reason, "error", cause)
}
