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

type Handler interface {
	Handle(ctx context.Context, msg *CreateRequest) (*Result, error)
}

// redis와 상호작용하는 구조체
type Consumer struct {
	rdb     *redis.Client
	cfg     config.Config
	handler Handler
	log     *slog.Logger
}

func NewConsumer(rdb *redis.Client, cfg config.Config, handler Handler, log *slog.Logger) *Consumer {
	return &Consumer{rdb: rdb, cfg: cfg, handler: handler, log: log}
}

// redis group이 생성되어 있지 않을 때 만드는 함수
func (c *Consumer) EnsureGroup(ctx context.Context) error {
	err := c.rdb.XGroupCreateMkStream(ctx, c.cfg.StreamKey(), c.cfg.ConsumerGroup(), "0").Err()
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
			Group:    c.cfg.ConsumerGroup(),
			Consumer: c.cfg.ConsumerName(),
			Streams:  []string{c.cfg.StreamKey(), ">"},
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
				c.log.Warn("consumer group missing, recreating", "stream", c.cfg.StreamKey(), "group", c.cfg.ConsumerGroup())
				if gerr := c.EnsureGroup(ctx); gerr != nil {
					c.log.Error("failed to recreate consumer group", "error", gerr)
				}
				continue
			}

			// 다음 데이터 읽기까지 주기 설정 후 대기 - queue/backoff에 로직 구현
			wait := bo.Next()
			c.log.Warn("XREADGROUP failed, retrying", "error", err, "wait", wait.String())
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
	// raw data를 CreateRequest로 변환
	msg, err := ParseCreateRequest(xmsg.Values)
	if err != nil {
		c.log.Error("poison message, moving to dead-letter stream", "id", xmsg.ID, "error", err)
		c.deadLetter(ctx, xmsg, err, 0)
		c.ack(ctx, xmsg.ID)
		return
	}

	// todo 응답 메시지 처리하는 로직 미구현
	result, err := c.handler.Handle(ctx, msg)
	if err != nil {
		c.log.Error("handler failed, leaving pending for retry",
			"id", xmsg.ID, "requestId", msg.RequestID, "error", err)
		return
	}

	// A permanent failure comes back as a non-nil result with Status=FAILED,
	// not as an error - it still needs to reach the backend, so publish it.
	if result != nil {
		c.publishResult(ctx, result)
	}

	c.ack(ctx, xmsg.ID)
}

// 결과를 redis stream에 저장
func (c *Consumer) publishResult(ctx context.Context, result *Result) {
	if err := c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: c.cfg.ResultStreamKey(),
		Values: result.ToStreamValues(),
	}).Err(); err != nil {
		c.log.Error("failed to publish result", "requestId", result.RequestID, "error", err)
		return
	}
	c.log.Info("published result",
		"requestId", result.RequestID, "status", result.Status,
		"host", result.Host, "port", result.Port)
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
			Stream:   c.cfg.StreamKey(),
			Group:    c.cfg.ConsumerGroup(),
			Consumer: c.cfg.ConsumerName(),
			MinIdle:  c.cfg.MinIdleTime,
			Start:    cursor,
			Count:    50,
		}).Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.log.Warn("XAUTOCLAIM failed, will retry next tick", "error", err)
			continue
		}
		cursor = next

		// 응답 실패 시 deadLetter 처리 - config에 설정된 횟수만큼 시도
		for _, xmsg := range claimed {
			deliveries := c.deliveryCount(ctx, xmsg.ID)
			if deliveries > c.cfg.MaxDeliveries {
				c.log.Error("max delivery attempts exceeded, moving to dead-letter stream",
					"id", xmsg.ID, "deliveries", deliveries, "maxDeliveries", c.cfg.MaxDeliveries)
				c.deadLetter(ctx, xmsg, errors.New("max delivery attempts exceeded"), deliveries)
				c.ack(ctx, xmsg.ID)
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
		Stream: c.cfg.StreamKey(),
		Group:  c.cfg.ConsumerGroup(),
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
		Stream: c.cfg.DeadLetterStreamKey(),
		Values: fields,
	}).Err(); err != nil {
		c.log.Error("failed to write to dead-letter stream, original entry will remain pending",
			"id", xmsg.ID, "error", err)
	}
}

func (c *Consumer) ack(ctx context.Context, id string) {
	if err := c.rdb.XAck(ctx, c.cfg.StreamKey(), c.cfg.ConsumerGroup(), id).Err(); err != nil {
		c.log.Error("XACK failed, message will be redelivered", "id", id, "error", err)
	}
}
