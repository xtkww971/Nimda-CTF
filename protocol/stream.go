package protocol

import (
	"fmt"
	"strconv"
)

// Redis Stream 위의 메시지 형식 (조율자 ↔ 에이전트).
// 스트림에 실리는 action 값. 웹 백엔드 → 조율자 큐가 쓰는 관례를 그대로 따른다.
const (
	ActionCreate  = "CREATE"
	ActionDestroy = "DESTROY"
)

// ── 조율자 → 에이전트 (생성 요청) ──────────────────────────────────────────

func (r CreateRequest) ToStreamValues() map[string]interface{} {
	values := map[string]interface{}{
		"action":           ActionCreate,
		"requestId":        r.RequestID,
		"challengeCode":    r.ChallengeCode,
		"userId":           strconv.FormatInt(r.UserID, 10),
		"isolationType":    r.IsolationType,
		"flagType":         r.FlagType,
		"buildContextPath": r.BuildContextPath,
	}
	// DYNAMIC일 때만 실린다. STATIC 문제에 빈 FLAG를 주입하지 않도록 있을 때만 넣는다.
	if r.Flag != "" {
		values["flag"] = r.Flag
	}
	return values
}

func ParseCreateRequest(values map[string]interface{}) (*CreateRequest, error) {
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

	buildContextPath, err := requiredString(values, "buildContextPath")
	if err != nil {
		return nil, err
	}

	return &CreateRequest{
		RequestID:        requestID,
		ChallengeCode:    challengeCode,
		UserID:           userID,
		IsolationType:    isolationType,
		FlagType:         flagType,
		Flag:             optionalString(values, "flag"),
		BuildContextPath: buildContextPath,
	}, nil
}

// ── 에이전트 → 조율자 (생성 결과) ──────────────────────────────────────────

func (r CreateResult) ToStreamValues() map[string]interface{} {
	values := map[string]interface{}{
		"requestId": r.RequestID,
		"status":    r.Status,
	}
	// 실패면 아래는 전부 비어 있다. 빈 값을 실어 보내면 받는 쪽이 "0번 포트"처럼 읽을 수
	// 있으므로, 있을 때만 싣는다.
	if r.InstanceID != "" {
		values["instanceId"] = r.InstanceID
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

func ParseCreateResult(values map[string]interface{}) (*CreateResult, error) {
	requestID, err := requiredString(values, "requestId")
	if err != nil {
		return nil, err
	}
	status, err := requiredString(values, "status")
	if err != nil {
		return nil, err
	}
	if status != StatusReady && status != StatusFailed {
		return nil, fmt.Errorf("field %q has unknown value %q", "status", status)
	}

	res := &CreateResult{
		RequestID:  requestID,
		Status:     status,
		InstanceID: optionalString(values, "instanceId"),
		Host:       optionalString(values, "host"),
		ExpiresAt:  optionalString(values, "expiresAt"),
		Message:    optionalString(values, "message"),
	}
	// 포트는 선택 필드다(실패 결과엔 없다). 있는데 숫자가 아니면 조용히 0으로 두지 않고
	// 알린다 — 접속 못 하는 인스턴스를 READY로 넘기는 게 제일 나쁘다.
	if raw := optionalString(values, "port"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("field %q is not a valid integer: %w", "port", err)
		}
		res.Port = port
	}
	return res, nil
}

// ── 조율자 → 에이전트 (삭제 요청) ──────────────────────────────────────────

func (r DestroyRequest) ToStreamValues() map[string]interface{} {
	values := map[string]interface{}{
		"action":     ActionDestroy,
		"requestId":  r.RequestID,
		"instanceId": r.InstanceID,
	}
	// 삭제 자체엔 필요 없지만 로그로 남기려고 싣는다(구 DestroyRequest 주석 참고).
	if r.ChallengeCode != "" {
		values["challengeCode"] = r.ChallengeCode
	}
	if r.UserID != 0 {
		values["userId"] = strconv.FormatInt(r.UserID, 10)
	}
	return values
}

func ParseDestroyRequest(values map[string]interface{}) (*DestroyRequest, error) {
	requestID, err := requiredString(values, "requestId")
	if err != nil {
		return nil, err
	}
	instanceID, err := requiredString(values, "instanceId")
	if err != nil {
		return nil, err
	}
	req := &DestroyRequest{
		RequestID:     requestID,
		InstanceID:    instanceID,
		ChallengeCode: optionalString(values, "challengeCode"),
	}
	if raw := optionalString(values, "userId"); raw != "" {
		userID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("field %q is not a valid integer: %w", "userId", err)
		}
		req.UserID = userID
	}
	return req, nil
}

// ── 에이전트 → 조율자 (삭제 결과) ──────────────────────────────────────────

func (r DestroyResult) ToStreamValues() map[string]interface{} {
	values := map[string]interface{}{
		"requestId": r.RequestID,
		"status":    r.Status,
	}
	if r.Message != "" {
		values["message"] = r.Message
	}
	return values
}

func ParseDestroyResult(values map[string]interface{}) (*DestroyResult, error) {
	requestID, err := requiredString(values, "requestId")
	if err != nil {
		return nil, err
	}
	status, err := requiredString(values, "status")
	if err != nil {
		return nil, err
	}
	switch status {
	case DestroyDone, DestroyNotFound, DestroyFailed:
	default:
		return nil, fmt.Errorf("field %q has unknown value %q", "status", status)
	}
	return &DestroyResult{
		RequestID: requestID,
		Status:    status,
		Message:   optionalString(values, "message"),
	}, nil
}

// ── 필드 헬퍼 ─────────────────────────────────────────────────────────────
// provisioner의 queue 패키지에 같은 모양의 헬퍼가 있지만 그쪽은 비공개다. 여기는 조율자와
// 에이전트가 함께 쓰는 모듈이라 따로 둔다.

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

// 없거나 문자열이 아니면 빈 문자열. 선택 필드용이라 에러로 보지 않는다.
func optionalString(values map[string]interface{}, key string) string {
	raw, ok := values[key]
	if !ok {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return s
}

// ── 에이전트 → 조율자 (결과 봉투) ──────────────────────────────────────────

// 웹 결과 스트림(queue.Result)은 challengeCode/userId를 요구하는데 CreateResult에는 없다.
// 소켓 시절엔 조율자가 요청을 손에 쥔 채 응답을 기다렸으니 알 수 있었지만, 스트림은
// 발행하고 끝이라 나중에 결과만 받아서는 "어느 문제의 누구였는지"를 복원할 수 없다.
// 그래서 에이전트가 그 둘을 되돌려준다.
type AgentResult struct {
	CreateResult
	ChallengeCode string
	UserID        int64
}

func (r AgentResult) ToStreamValues() map[string]interface{} {
	values := r.CreateResult.ToStreamValues()
	values["challengeCode"] = r.ChallengeCode
	values["userId"] = strconv.FormatInt(r.UserID, 10)
	return values
}

func ParseAgentResult(values map[string]interface{}) (*AgentResult, error) {
	base, err := ParseCreateResult(values)
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
	return &AgentResult{CreateResult: *base, ChallengeCode: challengeCode, UserID: userID}, nil
}
