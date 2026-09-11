package agentqueue

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	protocol "nimda-ctf-protocol"
)

// 요청을 처리해 결과를 남기는 함수
// 작업 실패 시 재시도를 할 수 있게 ACK는 하지 않게 설계
type Handler interface {
	Handle(ctx context.Context, req *protocol.RequestMessage) (*protocol.ResponseMessage, error)
}

type Options struct {
	Stream        string // 요청 스트림 (ctf:agent:ops)
	Group         string // 컨슈머 그룹 (ctf-agents)
	Consumer      string // 이 프로세스 이름
	ResultStream  string // 응답 스트림 (ctf:agent:results)
	BatchSize     int64
	BlockTimeout  time.Duration
	ReconnectBase time.Duration
	ReconnectMax  time.Duration
}

type Consumer struct {
	rdb     *redis.Client
	opts    Options
	handler Handler
	log     *slog.Logger
}

func NewConsumer(rdb *redis.Client, opts Options, handler Handler, log *slog.Logger) *Consumer {
	return &Consumer{rdb: rdb, opts: opts, handler: handler, log: log}
}

// 그룹이 존재하지 않으면 새로 만든다.
func (c *Consumer) EnsureGroup(ctx context.Context) error {
	err := c.rdb.XGroupCreateMkStream(ctx, c.opts.Stream, c.opts.Group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

func (c *Consumer) Run(ctx context.Context) {
	wait := c.opts.ReconnectBase

	for {
		if ctx.Err() != nil {
			return
		}

		streams, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    c.opts.Group,
			Consumer: c.opts.Consumer,
			Streams:  []string{c.opts.Stream, ">"},
			Count:    c.opts.BatchSize,
			Block:    c.opts.BlockTimeout,
		}).Result()

		if err != nil {
			if errors.Is(err, redis.Nil) { // 요청이 없는 경우
				wait = c.opts.ReconnectBase
				continue
			}
			if ctx.Err() != nil {
				return
			}
			// 그룹이 사라진 경우
			if strings.Contains(err.Error(), "NOGROUP") {
				c.log.Warn("consumer group missing, recreating",
					"stream", c.opts.Stream, "group", c.opts.Group)
				if gerr := c.EnsureGroup(ctx); gerr != nil {
					c.log.Error("failed to recreate consumer group", "error", gerr)
				}
				continue
			}

			c.log.Warn("XREADGROUP failed, retrying",
				"stream", c.opts.Stream, "error", err, "wait", wait.String())
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
			if wait *= 2; wait > c.opts.ReconnectMax {
				wait = c.opts.ReconnectMax
			}
			continue
		}

		wait = c.opts.ReconnectBase
		for _, stream := range streams {
			for _, msg := range stream.Messages {
				c.process(ctx, msg)
			}
		}
	}
}

func (c *Consumer) process(ctx context.Context, xmsg redis.XMessage) {
	req, err := protocol.ParseRequestMessage(xmsg.Values)
	if err != nil {
		// 해석이 안 되는 메시지
		// todo 데드레터 스트림. 지금은 로그가 유일한 흔적이다.
		c.log.Error("해석할 수 없는 요청, 버림",
			"stream", c.opts.Stream, "id", xmsg.ID, "error", err, "values", xmsg.Values)
		c.ackAndDelete(ctx, xmsg.ID)
		return
	}

	res, err := c.handler.Handle(ctx, req)
	if err != nil {
		// 일시적 실패. ACK하지 않고 두면 pending으로 남는다.
		// todo pending 회수(XAUTOCLAIM). 지금은 이 프로세스가 죽으면 그 메시지를
		//  아무도 다시 가져가지 않는다.
		c.log.Error("요청 처리 실패, ACK하지 않고 남김",
			"stream", c.opts.Stream, "id", xmsg.ID, "uuid", req.UUID, "error", err)
		return
	}

	if res != nil {
		c.publish(ctx, res)
	}
	c.ackAndDelete(ctx, xmsg.ID)
}

func (c *Consumer) publish(ctx context.Context, res *protocol.ResponseMessage) {
	if err := c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: c.opts.ResultStream,
		Values: res.ToStreamValues(),
	}).Err(); err != nil {
		// 여기서 실패하면 조율자는 결과를 영영 못 받는다. 요청은 이미 처리됐으므로
		// 재시도해도 같은 일을 또 하게 된다 — 지금은 알리는 것까지만 한다.
		c.log.Error("응답 발행 실패", "stream", c.opts.ResultStream,
			"uuid", res.UUID, "error", err)
		return
	}
	c.log.Info("응답 발행", "stream", c.opts.ResultStream,
		"uuid", res.UUID, "action", res.Action, "status", res.Status)
}

// 처리가 끝난 엔트리를 그룹에서 ACK하고 스트림에서도 지운다.
// XACK은 "이 그룹이 처리했다"는 표시일 뿐이라 엔트리는 스트림에 남는다.
// todo TTL 추가한 뒤 자동 만료되게 설정
func (c *Consumer) ackAndDelete(ctx context.Context, id string) {
	if err := c.rdb.XAck(ctx, c.opts.Stream, c.opts.Group, id).Err(); err != nil {
		c.log.Error("XACK failed, message will be redelivered", "id", id, "error", err)
	}
	if err := c.rdb.XDel(ctx, c.opts.Stream, id).Err(); err != nil {
		c.log.Warn("XDEL failed, entry stays in the stream", "id", id, "error", err)
	}
}
