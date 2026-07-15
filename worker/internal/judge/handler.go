// Package judge is where actual grading (fetch problem assets, compile,
// run in a sandbox, compare output, POST the result back to
// /api/judge/submission/result) will be implemented. Handler below is a
// placeholder so the queue skeleton is runnable end-to-end before that
// logic exists.
package judge

import (
	"context"
	"log/slog"

	"nimda-judge-worker/internal/queue"
)

type StubHandler struct {
	log *slog.Logger
}

func NewStubHandler(log *slog.Logger) *StubHandler {
	return &StubHandler{log: log}
}

// Handle currently only logs the submission. Replace this body with real
// judging logic; keep returning a non-nil error for anything retryable
// (e.g. transient S3/network failure) and let malformed input fail via
// queue.ParseSubmissionMessage instead of here.
func (h *StubHandler) Handle(ctx context.Context, msg *queue.SubmissionMessage) error {
	h.log.Info("received submission (stub handler, no-op)",
		"submissionId", msg.SubmissionID,
		"problemId", msg.ProblemID,
		"userId", msg.UserID,
		"language", msg.Language,
		"timeLimitSeconds", msg.TimeLimitSeconds,
		"memoryLimitMb", msg.MemoryLimitMB,
	)
	return nil
}