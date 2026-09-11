package queue

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"nimda-judge-provisioner/internal/config"
)

// 요청 하나를 처리하는 쪽. 인스턴스든 첨부파일이든 요청 타입이 하나라 핸들러도 하나다.
type Handler interface {
	Handle(ctx context.Context, msg *Request) (*Result, error)
}

// 어떤 스트림을 어떤 그룹으로 소비하고, 결과와 데드레터를 어디에 쓸지.
// 재시도·회수·데드레터 로직은 인스턴스 요청이든 다운로드 요청이든 똑같아서,
// 다른 건 이 주소들과 "메시지를 어떻게 해석해 처리하느냐"뿐이다.
type streamSpec struct {
	stream       string
	group        string
	resultStream string
	deadLetter   string
}

// 원본 필드 맵을 받아 처리하고 (결과 필드 맵, uuid, 에러)를 돌려준다.
// 결과가 nil이면 발행하지 않고, 에러면 ACK 없이 남겨 재시도한다.
type workFunc func(ctx context.Context, values map[string]interface{}) (map[string]interface{}, string, error)

// redis와 상호작용하는 구조체.
//
// rdb와 pub이 갈라져 있는 건 에이전트 결과 컨슈머 때문이다. 그쪽은 읽는 통과 쓰는 통이
// 다르다 — ctf-redis의 ctf:agent:results를 읽어서 웹이 보는 nimda-redis의 결과 스트림에
// 발행한다(ADR-0012). 다른 컨슈머는 둘이 같은 통이라 pub에 rdb를 그대로 넣는다.
//
// 소비(XREADGROUP/XACK/XAUTOCLAIM)와 데드레터는 항상 rdb 쪽이다. 데드레터는 "원본이 있던
// 통에 남긴다" — 못 처리한 메시지는 그 통의 문제고, 웹에 보낼 결과가 아니다.
type Consumer struct {
	rdb  *redis.Client // 소비할 스트림이 있는 통
	pub  *redis.Client // 결과를 발행할 통
	cfg  config.Config
	spec streamSpec
	work workFunc
	log  *slog.Logger
}

// 웹 백엔드 요청 컨슈머.
//
// 예전에는 인스턴스용과 다운로드용 두 개를 따로 만들었다. 스트림이 하나로 합쳐지면서
// 생성자도 하나가 됐다 — 무엇을 할지는 핸들러(Router)가 challengeCategory를 보고 정한다.
func NewConsumer(rdb *redis.Client, cfg config.Config, handler Handler, log *slog.Logger) *Consumer {
	spec := streamSpec{
		stream:       cfg.StreamKey(),
		group:        cfg.ConsumerGroup(),
		resultStream: cfg.ResultStreamKey(),
		deadLetter:   cfg.DeadLetterStreamKey(),
	}
	work := func(ctx context.Context, values map[string]interface{}) (map[string]interface{}, string, error) {
		msg, err := ParseRequest(values)
		if err != nil {
			return nil, "", err
		}

		// 요청 단위로 Logger에 Context(uuid) 주입
		reqLog := log.With(
			slog.String("uuid", msg.UUID),
			slog.String("stream", spec.stream),
		)
		reqLog.InfoContext(ctx, "processing request", "challengeCategory", msg.ChallengeCategory)

		result, err := handler.Handle(ctx, msg)
		if err != nil || result == nil {
			return nil, msg.UUID, err
		}

		reqLog.InfoContext(ctx, "successfully processed request")

		return result.ToStreamValues(), msg.UUID, nil
	}

	return &Consumer{rdb: rdb, pub: rdb, cfg: cfg, spec: spec, work: work, log: log}
}

// redis group이 생성되어 있지 않을 때 만드는 함수
func (c *Consumer) EnsureGroup(ctx context.Context) error {
	err := c.rdb.XGroupCreateMkStream(ctx, c.spec.stream, c.spec.group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

// consumer가 stream 데이터 읽게 하는 함수
func (c *Consumer) Run(ctx context.Context) {
	go c.reclaimLoop(ctx)

	bo := newBackoff(c.cfg.ReconnectBaseDelay, c.cfg.ReconnectMaxDelay)

	for {
		if ctx.Err() != nil {
			return
		}

		// config에 정의되어 있는 값으로 데이터를 읽음
		streams, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    c.spec.group,
			Consumer: c.cfg.ConsumerName(),
			Streams:  []string{c.spec.stream, ">"},
			Count:    c.cfg.BatchSize,
			Block:    c.cfg.BlockTimeout,
		}).Result()

		if err != nil {
			// 아직 데이터가 도착하지 않았을 때 처리
			if errors.Is(err, redis.Nil) {
				bo.Reset()
				continue
			}
			if ctx.Err() != nil {
				return
			}
			// 데이터 읽기를 시도했지만 그룹이 만들어져 있지 않을 때
			if strings.Contains(err.Error(), "NOGROUP") {
				c.log.Warn("consumer group missing, recreating", "stream", c.spec.stream, "group", c.spec.group)
				if gerr := c.EnsureGroup(ctx); gerr != nil {
					c.log.Error("failed to recreate consumer group", "error", gerr)
				}
				continue
			}

			// 다음 데이터 읽기까지 주기 설정 후 대기 - queue/backoff에 로직 구현
			wait := bo.Next()
			c.log.Warn("XREADGROUP failed, retrying", "stream", c.spec.stream, "error", err, "wait", wait.String())
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
			continue
		}

		bo.Reset()
		for _, stream := range streams {
			for _, msg := range stream.Messages {
				c.process(ctx, msg)
			}
		}
	}
}

// stream에서 raw data를 읽은 뒤 실행되는 함수
func (c *Consumer) process(ctx context.Context, xmsg redis.XMessage) {
	// raw data를 메시지로 해석
	fields, uuid, err := c.work(ctx, xmsg.Values)
	if err != nil {
		// 해석이 불가한 메시지는 잘못된 메시지이기에 바로 deadLetter로 이동
		if uuid == "" {
			c.log.Error("poison message, moving to dead-letter stream",
				"stream", c.spec.stream, "id", xmsg.ID, "error", err)
			c.deadLetter(ctx, xmsg, err, 0)
			c.ackAndDelete(ctx, xmsg.ID)
			return
		}
		c.log.Error("handler failed, leaving pending for retry",
			"stream", c.spec.stream, "id", xmsg.ID, "uuid", uuid, "error", err)
		return
	}

	// 영구 실패는 error가 아니라 Status=FAILED인 결과로 온다. 이것도 백엔드까지 가야 하니 발행한다.
	if fields != nil {
		c.publishResult(ctx, fields, uuid)
	}

	c.ackAndDelete(ctx, xmsg.ID)
}

// 결과를 redis stream에 저장
func (c *Consumer) publishResult(ctx context.Context, fields map[string]interface{}, uuid string) {
	if err := c.pub.XAdd(ctx, &redis.XAddArgs{
		Stream: c.spec.resultStream,
		Values: fields,
	}).Err(); err != nil {
		c.log.Error("failed to publish result",
			"stream", c.spec.resultStream, "uuid", uuid, "error", err)
		return
	}
	c.log.Info("published result",
		"stream", c.spec.resultStream, "uuid", uuid, "status", fields["status"])
}

// stream에서 데이터를 읽는 함수
// 자료구조 특성 상 데이터 삭제를 직접 해야 하기 때문에 지금은 cursor로 계산해서 읽어옴
// todo stream 데이터 삭제 로직 구현(여기에 말고 웹사이트에)
func (c *Consumer) reclaimLoop(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.ClaimInterval)
	defer ticker.Stop()

	cursor := "0-0"
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		claimed, next, err := c.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   c.spec.stream,
			Group:    c.spec.group,
			Consumer: c.cfg.ConsumerName(),
			MinIdle:  c.cfg.MinIdleTime,
			Start:    cursor,
			Count:    50,
		}).Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.log.Warn("XAUTOCLAIM failed, will retry next tick", "stream", c.spec.stream, "error", err)
			continue
		}
		cursor = next

		// 응답 실패 시 deadLetter 처리 - config에 설정된 횟수만큼 시도
		for _, xmsg := range claimed {
			deliveries := c.deliveryCount(ctx, xmsg.ID)
			if deliveries > c.cfg.MaxDeliveries {
				c.log.Error("max delivery attempts exceeded, moving to dead-letter stream",
					"stream", c.spec.stream, "id", xmsg.ID, "deliveries", deliveries, "maxDeliveries", c.cfg.MaxDeliveries)
				c.deadLetter(ctx, xmsg, errors.New("max delivery attempts exceeded"), deliveries)
				c.ackAndDelete(ctx, xmsg.ID)
				continue
			}
			c.log.Warn("reclaimed stale pending message, retrying", "id", xmsg.ID, "deliveries", deliveries)
			c.process(ctx, xmsg)
		}
	}
}

// stream의 data id로 전송 시도 횟수 읽어오기
func (c *Consumer) deliveryCount(ctx context.Context, id string) int64 {
	pending, err := c.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: c.spec.stream,
		Group:  c.spec.group,
		Start:  id,
		End:    id,
		Count:  1,
	}).Result()
	if err != nil || len(pending) == 0 {
		// 정상적으로 전송에 성공
		return 1
	}
	// 전송 실패 시 횟수 반환
	return pending[0].RetryCount
}

// todo DeadLetterStream 정책 구현 - ctf 서버에서 웹 서버로 전송만 할 뿐 ctf 내부에는 redis 컨테이너 미존재(띄우지 않을 것)
func (c *Consumer) deadLetter(ctx context.Context, xmsg redis.XMessage, cause error, deliveries int64) {
	// 기존 메시지 사유를 추가하기 위해 len+3으로 진행
	fields := make(map[string]interface{}, len(xmsg.Values)+3)
	for k, v := range xmsg.Values {
		fields[k] = v
	}
	fields["originalId"] = xmsg.ID
	fields["failureReason"] = cause.Error()
	fields["deliveryCount"] = deliveries

	// todo DeadLetterStream은 웹사이트에도 미구현 따라서 통신 규격을 맞춰야 함 - ctf 서버에서는 redis 인스턴스를 넣지 않음
	if err := c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: c.spec.deadLetter,
		Values: fields,
	}).Err(); err != nil {
		c.log.Error("failed to write to dead-letter stream, original entry will remain pending",
			"id", xmsg.ID, "error", err)
	}
}

// 메시지 읽기에 성공 시 nimda-redis에서 메시지를 제거
// 다 끝난 엔트리를 그룹에서 ACK하고 스트림에서도 지운다.
//
// XACK은 "이 컨슈머 그룹이 처리했다"는 표시일 뿐이라 엔트리 자체는 스트림에 남는다.
// Redis Stream에는 자동 만료가 없어서, 안 지우면 요청을 받는 만큼 무한정 자란다.
// (pending은 0인데 XLEN만 계속 늘어나는 모양으로 나타난다.)
//
// ACK을 먼저 하는 이유: 지우고 나서 죽으면 없는 엔트리를 가리키는 pending 항목이 남는다.
// 반대 순서면 중간에 죽어도 엔트리 하나가 덜 지워질 뿐이고, 그건 다음번에 같은 자리를
// 지날 일이 없으니 그냥 남는다 — 둘 중 덜 나쁜 쪽이다.
//
// 주의: XDEL은 스트림에서 통째로 지우므로 다른 컨슈머 그룹도 그 엔트리를 못 본다.
// 지금 이 스트림들은 그룹이 하나씩뿐이라 괜찮지만, 나중에 감사·재생 목적으로 그룹을
// 하나 더 붙이면 이 삭제부터 다시 봐야 한다.
func (c *Consumer) ackAndDelete(ctx context.Context, id string) {
	c.ack(ctx, id)

	if err := c.rdb.XDel(ctx, c.spec.stream, id).Err(); err != nil {
		// 못 지워도 처리 자체는 끝났다. 엔트리가 남을 뿐이라 실패로 보지 않는다.
		c.log.Warn("XDEL failed, entry stays in the stream",
			"stream", c.spec.stream, "id", id, "error", err)
	}
}

func (c *Consumer) ack(ctx context.Context, id string) {
	if err := c.rdb.XAck(ctx, c.spec.stream, c.spec.group, id).Err(); err != nil {
		c.log.Error("XACK failed, message will be redelivered", "id", id, "error", err)
	}
}
