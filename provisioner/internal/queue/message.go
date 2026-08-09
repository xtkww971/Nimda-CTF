package queue

import (
	"fmt"
	"strconv"
)

// SubmissionMessage mirrors judgeServer.domain.submission.mq.SubmissionMessage
// on the producer side (backend-spring). Field names/types are the contract
// documented in docs/judge-queue-contract.md - changing them requires
// updating both sides.
type SubmissionMessage struct {
	SubmissionID     int64
	ProblemID        int64
	UserID           int64
	Language         string
	SourceCode       string
	TimeLimitSeconds float64
	MemoryLimitMB    int64
	SubmittedAt      string
}

// ParseSubmissionMessage converts a raw Redis Stream entry (XMessage.Values)
// into a SubmissionMessage. A non-nil error here means the entry itself is
// malformed (missing/invalid field) - retrying delivery will never fix it,
// so callers should treat this as a poison message, not a transient failure.
func ParseSubmissionMessage(values map[string]interface{}) (*SubmissionMessage, error) {
	submissionID, err := requiredInt64(values, "submissionId")
	if err != nil {
		return nil, err
	}
	problemID, err := requiredInt64(values, "problemId")
	if err != nil {
		return nil, err
	}
	userID, err := requiredInt64(values, "userId")
	if err != nil {
		return nil, err
	}
	language, err := requiredString(values, "language")
	if err != nil {
		return nil, err
	}
	sourceCode, err := requiredString(values, "sourceCode")
	if err != nil {
		return nil, err
	}
	timeLimitSeconds, err := requiredFloat64(values, "timeLimitSeconds")
	if err != nil {
		return nil, err
	}
	memoryLimitMB, err := requiredInt64(values, "memoryLimitMb")
	if err != nil {
		return nil, err
	}
	submittedAt, err := requiredString(values, "submittedAt")
	if err != nil {
		return nil, err
	}

	return &SubmissionMessage{
		SubmissionID:     submissionID,
		ProblemID:        problemID,
		UserID:           userID,
		Language:         language,
		SourceCode:       sourceCode,
		TimeLimitSeconds: timeLimitSeconds,
		MemoryLimitMB:    memoryLimitMB,
		SubmittedAt:      submittedAt,
	}, nil
}

func requiredString(values map[string]interface{}, key string) (string, error) {
	raw, ok := values[key]
	if !ok {
		return "", fmt.Errorf("field %q is missing", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("field %q has unexpected type %T", key, raw)
	}
	if s == "" {
		return "", fmt.Errorf("field %q is empty", key)
	}
	return s, nil
}

func requiredInt64(values map[string]interface{}, key string) (int64, error) {
	s, err := requiredString(values, key)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("field %q is not a valid integer: %w", key, err)
	}
	return n, nil
}

func requiredFloat64(values map[string]interface{}, key string) (float64, error) {
	s, err := requiredString(values, key)
	if err != nil {
		return 0, err
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("field %q is not a valid number: %w", key, err)
	}
	return f, nil
}