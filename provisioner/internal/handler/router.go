package handler

import (
	"context"
	"log/slog"

	"nimda-judge-provisioner/internal/queue"
)

// 작업 메시지를 받아 실제로 처리하는 쪽.
type TaskHandler interface {
	Handle(ctx context.Context, msg TaskMessage) (*queue.Result, error)
}

// 스트림에서 올라온 요청을 분배하는 모듈
type Router struct {
	work TaskHandler
	log  *slog.Logger
}

func NewRouter(work TaskHandler, log *slog.Logger) *Router {
	return &Router{work: work, log: log}
}

var _ queue.Handler = (*Router)(nil)

func (r *Router) Handle(ctx context.Context, msg *queue.Request) (*queue.Result, error) {
	task := newTaskMessage(msg)

	r.log.InfoContext(ctx, "classified request",
		"uuid", task.ID(), "challengeCategory", msg.ChallengeCategory, "task", task.Kind())

	return r.work.Handle(ctx, task)
}
