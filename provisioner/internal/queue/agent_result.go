package queue

import (
	"context"
	"log/slog"

	"github.com/redis/go-redis/v9"

	protocol "nimda-ctf-protocol"

	"nimda-judge-provisioner/internal/config"
)

// 에이전트가 인스턴스를 다 만들고 돌려준 결과를 받는 쪽.
//
// 예전에는 에이전트가 결과에 challengeCode/userId를 되실어 보내야 했다(protocol.AgentResult).
// 웹이 보는 결과가 그 둘을 요구했기 때문인데, 지금은 웹도 UUID 하나로 자기 요청을 찾는다.
// 그래서 봉투가 사라지고 에이전트의 결과를 거의 그대로 옮기면 된다.
type AgentResultHandler interface {
	Handle(ctx context.Context, res *protocol.ResponseMessage) (*Result, error)
}

// 에이전트 결과(ctf:agent:results) 컨슈머.
//
// 이 컨슈머만 통이 둘이다. agentRdb는 조율자·에이전트 전용 ctf-redis고, webRdb는 웹 백엔드와
// 공유하는 nimda-redis다(ADR-0012의 "웹이 쓰는 Redis와 분리한다"). 두 통을 잇는 것이
// 조율자의 역할이므로, 그 이음매가 여기 한 곳에 모인다.
func NewAgentResultConsumer(agentRdb, webRdb *redis.Client, cfg config.Config, handler AgentResultHandler, log *slog.Logger) *Consumer {
	spec := streamSpec{
		stream:       cfg.AgentResultStreamKey(),
		group:        cfg.AgentConsumerGroup(),
		resultStream: cfg.ResultStreamKey(),
		deadLetter:   cfg.AgentDeadLetterStreamKey(),
	}
	work := func(ctx context.Context, values map[string]interface{}) (map[string]interface{}, string, error) {
		res, err := protocol.ParseResponseMessage(values)
		if err != nil {
			return nil, "", err
		}

		reqLog := log.With(
			slog.String("uuid", res.UUID),
			slog.String("stream", spec.stream),
		)
		reqLog.InfoContext(ctx, "received agent result", "action", res.Action, "status", res.Status)

		result, err := handler.Handle(ctx, res)
		if err != nil || result == nil {
			return nil, res.UUID, err
		}
		return result.ToStreamValues(), res.UUID, nil
	}
	return &Consumer{rdb: agentRdb, pub: webRdb, cfg: cfg, spec: spec, work: work, log: log}
}
