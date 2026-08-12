package queue

import "testing"

func validCreateFields() map[string]interface{} {
	return map[string]interface{}{
		"action":        "CREATE",
		"requestId":     "req-abc",
		"challengeCode": "web-01",
		"userId":        "42",
		"isolationType": "SHARED",
		"flagType":      "STATIC",
		"requestedAt":   "2026-08-12T10:00:00",
	}
}

func TestParseCreateRequest(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]interface{})
		wantErr bool
	}{
		{"valid", func(map[string]interface{}) {}, false},
		{"missing challengeCode", func(m map[string]interface{}) { delete(m, "challengeCode") }, true},
		{"empty requestId", func(m map[string]interface{}) { m["requestId"] = "" }, true},
		{"non-numeric userId", func(m map[string]interface{}) { m["userId"] = "abc" }, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := validCreateFields()
			tc.mutate(input)

			msg, err := ParseCreateRequest(input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if msg.ChallengeCode != "web-01" || msg.UserID != 42 || msg.IsolationType != "SHARED" {
				t.Errorf("parsed fields wrong: %+v", msg)
			}
		})
	}
}

func TestResultToStreamValuesReadyIncludesHostPort(t *testing.T) {
	r := Result{
		RequestID:     "req-abc",
		ChallengeCode: "web-01",
		UserID:        42,
		Status:        StatusReady,
		Host:          "chal.nimda.kr",
		Port:          31337,
		ExpiresAt:     "2026-08-12T12:00:00Z",
	}
	v := r.ToStreamValues()

	if v["status"] != StatusReady || v["host"] != "chal.nimda.kr" || v["port"] != "31337" {
		t.Errorf("READY result missing access fields: %+v", v)
	}
	if v["userId"] != "42" {
		t.Errorf("userId = %v, want \"42\"", v["userId"])
	}
	if _, ok := v["message"]; ok {
		t.Errorf("READY result should not carry a message field")
	}
}

func TestResultToStreamValuesFailedOmitsHostPort(t *testing.T) {
	r := Result{
		RequestID:     "req-999",
		ChallengeCode: "web-01",
		UserID:        9,
		Status:        StatusFailed,
		Message:       "image build failed",
	}
	v := r.ToStreamValues()

	if v["status"] != StatusFailed || v["message"] != "image build failed" {
		t.Errorf("FAILED result missing status/message: %+v", v)
	}
	if _, ok := v["host"]; ok {
		t.Errorf("FAILED result should omit host")
	}
	if _, ok := v["port"]; ok {
		t.Errorf("FAILED result should omit port")
	}
}
