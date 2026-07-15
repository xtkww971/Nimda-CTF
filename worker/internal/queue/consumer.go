package queue

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"nimda-judge-worker/internal/config"
)

// Handler processes one submission. Returning an error means "retry me
// later" - the message is left un-ACKed and will be picked up again by the
// reclaim loop (see Consumer.reclaimLoop). Handler implementations should be
// idempotent: Redis Streams consumer groups give at-least-once delivery, so
// the same submission can be handled more than once (e.g. the worker
// crashes after judging but before XACK reaches Redis).
type Handler interface {
	Handle(ctx context.Context, msg *SubmissionMessage) error
}

// Consumer reads judge submissions from a Redis Stream consumer group,
// dispatches them to a Handler, and manages the failure/retry lifecycle
// per docs/redis-stream-queue-design.md.
type Consumer struct {
	rdb     *redis.Client
	cfg     config.Config
	handler Handler
	log     *slog.Logger
}

func NewConsumer(rdb *redis.Client, cfg config.Config, handler Handler, log *slog.Logger) *Consumer {
	return &Consumer{rdb: rdb, cfg: cfg, handler: handler, log: log}
}

// EnsureGroup creates the stream + consumer group if they don't exist yet.
// It is idempotent: BUSYGROUP (group already exists) is not an error.
// The producer (backend-spring) also does this on startup, so either side
// coming up first is fine.
func (c *Consumer) EnsureGroup(ctx context.Context) error {
	err := c.rdb.XGroupCreateMkStream(ctx, c.cfg.StreamKey, c.cfg.ConsumerGroup, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

// Run blocks, reading new messages until ctx is cancelled. A background
// goroutine concurrently reclaims stale pending entries (crashed/stuck
// consumers) so a single worker crash never permanently strands work.
func (c *Consumer) Run(ctx context.Context) {
	go c.reclaimLoop(ctx)

	bo := newBackoff(c.cfg.ReconnectBaseDelay, c.cfg.ReconnectMaxDelay)

	for {
		if ctx.Err() != nil {
			return
		}

		streams, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    c.cfg.ConsumerGroup,
			Consumer: c.cfg.ConsumerName,
			Streams:  []string{c.cfg.StreamKey, ">"},
			Count:    c.cfg.BatchSize,
			Block:    c.cfg.BlockTimeout,
		}).Result()

		if err != nil {
			if errors.Is(err, redis.Nil) {
				// Block timeout elapsed with nothing new - expected, not an error.
				bo.Reset()
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if strings.Contains(err.Error(), "NOGROUP") {
				c.log.Warn("consumer group missing, recreating", "stream", c.cfg.StreamKey, "group", c.cfg.ConsumerGroup)
				if gerr := c.EnsureGroup(ctx); gerr != nil {
					c.log.Error("failed to recreate consumer group", "error", gerr)
				}
				continue
			}

			// Transient error (connection reset, Redis restarting, network
			// partition, ...). Back off and retry - the consumer group's
			// last-delivered-id lives server-side in Redis, so we lose
			// nothing by waiting: unread messages stay unread and any
			// message we had in-flight stays in the Pending Entries List
			// for the reclaim loop to pick up.
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

// process handles a single delivery and decides its outcome:
//   - malformed message (poison)   -> dead-letter immediately, ACK
//   - handler error (transient)    -> leave un-ACKed for retry
//   - success                      -> ACK
func (c *Consumer) process(ctx context.Context, xmsg redis.XMessage) {
	msg, err := ParseSubmissionMessage(xmsg.Values)
	if err != nil {
		c.log.Error("poison message, moving to dead-letter stream", "id", xmsg.ID, "error", err)
		c.deadLetter(ctx, xmsg, err, 0)
		c.ack(ctx, xmsg.ID)
		return
	}

	if err := c.handler.Handle(ctx, msg); err != nil {
		c.log.Error("handler failed, leaving pending for retry",
			"id", xmsg.ID, "submissionId", msg.SubmissionID, "error", err)
		return
	}

	c.ack(ctx, xmsg.ID)
}

// reclaimLoop periodically scans the Pending Entries List for messages that
// have been idle (delivered but never ACKed) longer than MinIdleTime and
// re-claims them for this consumer. This is what recovers work from a
// worker process that crashed, was OOM-killed, or lost its network
// connection mid-processing - Redis has no server-side timeout of its own,
// so a claim-based sweep is required per the official Streams docs.
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
			Stream:   c.cfg.StreamKey,
			Group:    c.cfg.ConsumerGroup,
			Consumer: c.cfg.ConsumerName,
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

// deliveryCount looks up how many times this exact entry has been delivered
// (XCLAIM/XAUTOCLAIM increment Redis's own counter), so retry limits survive
// worker restarts instead of being tracked only in local memory.
func (c *Consumer) deliveryCount(ctx context.Context, id string) int64 {
	pending, err := c.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: c.cfg.StreamKey,
		Group:  c.cfg.ConsumerGroup,
		Start:  id,
		End:    id,
		Count:  1,
	}).Result()
	if err != nil || len(pending) == 0 {
		// Entry no longer pending (e.g. raced with another claimer and got
		// ACKed already) - treat as first delivery so we don't dead-letter
		// on a lookup failure alone.
		return 1
	}
	return pending[0].RetryCount
}

// deadLetter preserves the original fields plus failure context in a
// separate stream so operators can inspect/replay it without it blocking
// the main queue. See docs/redis-stream-queue-design.md section 5.
func (c *Consumer) deadLetter(ctx context.Context, xmsg redis.XMessage, cause error, deliveries int64) {
	fields := make(map[string]interface{}, len(xmsg.Values)+3)
	for k, v := range xmsg.Values {
		fields[k] = v
	}
	fields["originalId"] = xmsg.ID
	fields["failureReason"] = cause.Error()
	fields["deliveryCount"] = deliveries

	if err := c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: c.cfg.DeadLetterStreamKey,
		Values: fields,
	}).Err(); err != nil {
		c.log.Error("failed to write to dead-letter stream, original entry will remain pending",
			"id", xmsg.ID, "error", err)
	}
}

func (c *Consumer) ack(ctx context.Context, id string) {
	if err := c.rdb.XAck(ctx, c.cfg.StreamKey, c.cfg.ConsumerGroup, id).Err(); err != nil {
		// The message was already processed (or dead-lettered); if XACK
		// itself fails (e.g. Redis dropped the connection at this exact
		// moment) the entry simply stays pending and reclaimLoop will pick
		// it up again. Handlers must be idempotent for this reason.
		c.log.Error("XACK failed, message will be redelivered", "id", id, "error", err)
	}
}