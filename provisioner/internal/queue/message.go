package queue

import (
	"fmt"
	"strconv"
)

type CreateRequest struct {
	Action        string // todo 모듈을 분리해 Action에 따라 작업을 분배해야 함
	RequestID     string
	ChallengeCode string // S3 객체 경로
	UserID        int64
	IsolationType string // "SHARED" | "PER_USER" - todo IsolateionType에 따라 포트 분배 방식이 달라져야 함
	FlagType      string // "STATIC" | "DYNAMIC"
	RequestedAt   string
}

// redis로 받은 데이터 raw data를 CreateRequest로 변환
func ParseCreateRequest(values map[string]interface{}) (*CreateRequest, error) {
	action, err := requiredString(values, "action")
	if err != nil {
		return nil, err
	}
	requestID, err := requiredString(values, "requestId")
	if err != nil {
		return nil, err
	}
	challengeCode, err := requiredString(values, "challengeCode")
	if err != nil {
		return nil, err
	}
	userID, err := requiredInt64(values, "userId")
	if err != nil {
		return nil, err
	}
	isolationType, err := requiredString(values, "isolationType")
	if err != nil {
		return nil, err
	}
	flagType, err := requiredString(values, "flagType")
	if err != nil {
		return nil, err
	}
	requestedAt, err := requiredString(values, "requestedAt")
	if err != nil {
		return nil, err
	}

	return &CreateRequest{
		Action:        action,
		RequestID:     requestID,
		ChallengeCode: challengeCode,
		UserID:        userID,
		IsolationType: isolationType,
		FlagType:      flagType,
		RequestedAt:   requestedAt,
	}, nil
}

// todo 결과 반환 로직 미구현
const (
	StatusReady  = "READY"
	StatusFailed = "FAILED"
)

// todo 결과 반환 로직 미구현 일단 웹사이트와 데이터 구조만 맞춰놓은 상태
type Result struct {
	RequestID     string
	ChallengeCode string
	UserID        int64
	Status        string // StatusReady | StatusFailed
	Host          string // 실패 시 empty
	Port          int    // 실패 시 0
	ExpiresAt     string // todo 연결 종료 시간 설정 구현해야 함
	Message       string // 실패 사유
}

// 결과를 redis stream에 넣는 함수
func (r Result) ToStreamValues() map[string]interface{} {
	values := map[string]interface{}{
		"requestId":     r.RequestID,
		"challengeCode": r.ChallengeCode,
		"userId":        strconv.FormatInt(r.UserID, 10),
		"status":        r.Status,
	}
	if r.Host != "" {
		values["host"] = r.Host
	}
	if r.Port != 0 {
		values["port"] = strconv.Itoa(r.Port)
	}
	if r.ExpiresAt != "" {
		values["expiresAt"] = r.ExpiresAt
	}
	if r.Message != "" {
		values["message"] = r.Message
	}
	return values
}

// helper 함수들
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
