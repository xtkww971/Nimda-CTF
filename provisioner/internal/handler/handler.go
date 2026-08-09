// Package handler is where the Provisioner acts on a message pulled off the
// instance-operations queue (create / destroy a challenge instance by talking
// to the Agent over its unix socket - see ADR-0007). StubHandler below is a
// placeholder so the queue skeleton is runnable end-to-end before that logic
// exists; it is replaced in T3 (WU-35/36).
package handler

import (
	"context"
	"log/slog"

	"nimda-judge-provisioner/internal/queue"
)

type StubHandler struct {
	log *slog.Logger
}

func NewStubHandler(log *slog.Logger) *StubHandler {
	return &StubHandler{log: log}
}

// Handle currently only logs the message. Replace this body with the real
// provisioning logic; keep returning a non-nil error for anything retryable
// (e.g. the Agent socket being temporarily unreachable) and let malformed
// input fail via queue.ParseSubmissionMessage instead of here.
func (h *StubHandler) Handle(ctx context.Context, msg *queue.SubmissionMessage) error {
	h.log.Info("received message (stub handler, no-op)",
		"submissionId", msg.SubmissionID,
		"problemId", msg.ProblemID,
		"userId", msg.UserID,
		"language", msg.Language,
		"timeLimitSeconds", msg.TimeLimitSeconds,
		"memoryLimitMb", msg.MemoryLimitMB,
	)
	return nil
}
