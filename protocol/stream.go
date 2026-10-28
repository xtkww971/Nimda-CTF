package protocol

import (
	"fmt"
	"strconv"
)

// Redis Stream 파싱
// DESTROY는 UUID만 필요

// Provisioner -> Agent

func (r RequestMessage) ToStreamValues() map[string]interface{} {
	values := map[string]interface{}{
		"uuid":   r.UUID,
		"action": r.Action,
	}
	if r.ChallengeCode != "" {
		values["challengeCode"] = r.ChallengeCode
	}
	if r.IsolationType != "" {
		values["isolationType"] = string(r.IsolationType)
	}
	if r.AccessType != "" {
		values["accessType"] = string(r.AccessType)
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
		IsolationType:    IsolationType(optionalString(values, "isolationType")),
		AccessType:       AccessType(optionalString(values, "accessType")),
		FlagType:         optionalString(values, "flagType"),
		Flag:             optionalString(values, "flag"),
		BuildContextPath: optionalString(values, "buildContextPath"),
	}

	switch action {
	case ActionCreate:
		for _, f := range []struct{ name, value string }{
			{"challengeCode", req.ChallengeCode},
			{"isolationType", string(req.IsolationType)},
			{"accessType", string(req.AccessType)},
			{"flagType", req.FlagType},
			{"buildContextPath", req.BuildContextPath},
		} {
			if f.value == "" {
				return nil, fmt.Errorf("field %q is required for action %q", f.name, action)
			}
		}

		if !req.IsolationType.Valid() {
			return nil, fmt.Errorf("field %q has unknown value %q", "isolationType", req.IsolationType)
		}
		if !req.AccessType.Valid() {
			return nil, fmt.Errorf("field %q has unknown value %q", "accessType", req.AccessType)
		}
		if req.IsolationType == IsolationShared && req.FlagType == FlagDynamic {
			return nil, fmt.Errorf("isolationType %q cannot use flagType %q", IsolationShared, FlagDynamic)
		}
		// pwn(TCP)은 포트를 밖에 연다. SHARED로 뜨면 한 명이 셸을 따서 모두의 문제를 망친다.
		if req.AccessType == AccessTCP && req.IsolationType == IsolationShared {
			return nil, fmt.Errorf("accessType %q cannot use isolationType %q", AccessTCP, IsolationShared)
		}
	case ActionDestroy:
		// 어느 컨테이너인지는 에이전트가 uuid로 찾는다.
	default:
		return nil, fmt.Errorf("field %q has unknown value %q", "action", action)
	}

	return req, nil
}

// Agent -> Provisioner

func (r ResponseMessage) ToStreamValues() map[string]interface{} {
	values := map[string]interface{}{
		"uuid":   r.UUID,
		"action": r.Action,
		"status": r.Status,
	}
	// 실패인 경우 전부 비어 있다
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
