package queue

import "fmt"

// 스트림 값은 전부 문자열이다. 파싱 헬퍼만 여기 둔다.
//
// 예전에는 인스턴스와 다운로드 두 메시지가 공유하는 "머리 필드"(requestHeader)가 있었다.
// 요청이 하나로 합쳐지면서 그 개념이 사라졌다 — 지금은 Request 하나가 전부다.

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
