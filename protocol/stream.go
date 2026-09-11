package protocol

import (
	"fmt"
	"strconv"
)

// Redis Stream 위의 메시지 변환 (조율자 ↔ 에이전트).
// 필수/선택이 Action에 따라 갈린다. CREATE는 빌드에 필요한 필드가 다 있어야 하고,
// DESTROY는 UUID만 있으면 된다. 그래서 파싱은 먼저 action을 읽고 그에 맞춰 검사한다.

// ── 조율자 → 에이전트 ──────────────────────────────────────────────────────

func (r RequestMessage) ToStreamValues() map[string]interface{} {
	values := map[string]interface{}{
		"uuid":   r.UUID,
		"action": r.Action,
	}
	// 빈 값은 키째로 뺀다. DESTROY에 빈 challengeCode가 실려 있으면 받는 쪽이
	// "코드가 빈 문제"로 읽는다.
	if r.ChallengeCode != "" {
		values["challengeCode"] = r.ChallengeCode
	}
	if r.IsolationType != "" {
		values["isolationType"] = r.IsolationType
	}
	if r.FlagType != "" {
		values["flagType"] = r.FlagType
	}
	// DYNAMIC일 때만 실린다. STATIC 문제에 빈 FLAG를 주입하지 않도록 있을 때만 넣는다.
	if r.Flag != "" {
		values["flag"] = r.Flag
	}
	if r.BuildContextPath != "" {
		values["buildContextPath"] = r.BuildContextPath
	}
	return values
}

func ParseRequestMessage(values map[string]interface{}) (*RequestMessage, error) {
	uuid, err := requiredString(values, "uuid")
	if err != nil {
		return nil, err
	}
	action, err := requiredString(values, "action")
	if err != nil {
		return nil, err
	}

	req := &RequestMessage{
		UUID:             uuid,
		Action:           action,
		ChallengeCode:    optionalString(values, "challengeCode"),
		IsolationType:    optionalString(values, "isolationType"),
		FlagType:         optionalString(values, "flagType"),
		Flag:             optionalString(values, "flag"),
		BuildContextPath: optionalString(values, "buildContextPath"),
	}

	switch action {
	case ActionCreate:
		for _, f := range []struct{ name, value string }{
			{"challengeCode", req.ChallengeCode},
			{"isolationType", req.IsolationType},
			{"flagType", req.FlagType},
			{"buildContextPath", req.BuildContextPath},
		} {
			if f.value == "" {
				return nil, fmt.Errorf("field %q is required for action %q", f.name, action)
			}
		}
	case ActionDestroy:
		// 어느 컨테이너인지는 에이전트가 uuid로 찾는다. 더 요구할 것이 없다.
	default:
		return nil, fmt.Errorf("field %q has unknown value %q", "action", action)
	}

	return req, nil
}

// ── 에이전트 → 조율자 ──────────────────────────────────────────────────────

func (r ResponseMessage) ToStreamValues() map[string]interface{} {
	values := map[string]interface{}{
		"uuid":   r.UUID,
		"action": r.Action,
		"status": r.Status,
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

func ParseResponseMessage(values map[string]interface{}) (*ResponseMessage, error) {
	uuid, err := requiredString(values, "uuid")
	if err != nil {
		return nil, err
	}
	action, err := requiredString(values, "action")
	if err != nil {
		return nil, err
	}
	status, err := requiredString(values, "status")
	if err != nil {
		return nil, err
	}
	switch status {
	case StatusReady, StatusFailed, StatusDestroyed, StatusNotFound:
	default:
		return nil, fmt.Errorf("field %q has unknown value %q", "status", status)
	}

	res := &ResponseMessage{
		UUID:       uuid,
		Action:     action,
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

// ── 필드 헬퍼 ─────────────────────────────────────────────────────────────

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
