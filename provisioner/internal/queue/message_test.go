package queue

import "testing"

// BenchmarkParseSubmissionMessage gives the optimization track a starting
// point for measuring hot-path allocations instead of guessing - run via
// scripts\bench.bat (or `go test -run=^$ -bench=. -benchmem ./...`).
func BenchmarkParseSubmissionMessage(b *testing.B) {
	values := map[string]interface{}{
		"submissionId":     "1001",
		"problemId":        "42",
		"userId":           "7",
		"language":         "JAVA",
		"sourceCode":       "class Main {}",
		"timeLimitSeconds": "2.5",
		"memoryLimitMb":    "256",
		"submittedAt":      "2026-07-17T10:00:00",
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseSubmissionMessage(values); err != nil {
			b.Fatal(err)
		}
	}
}

func TestParseSubmissionMessage(t *testing.T) {
	valid := map[string]interface{}{
		"submissionId":     "1001",
		"problemId":        "42",
		"userId":           "7",
		"language":         "JAVA",
		"sourceCode":       "class Main {}",
		"timeLimitSeconds": "2.5",
		"memoryLimitMb":    "256",
		"submittedAt":      "2026-07-17T10:00:00",
	}

	tests := []struct {
		name    string
		mutate  func(map[string]interface{})
		wantErr bool
	}{
		{
			name:    "valid message parses all fields",
			mutate:  func(m map[string]interface{}) {},
			wantErr: false,
		},
		{
			name: "missing required field is rejected",
			mutate: func(m map[string]interface{}) {
				delete(m, "sourceCode")
			},
			wantErr: true,
		},
		{
			name: "empty string field is rejected",
			mutate: func(m map[string]interface{}) {
				m["language"] = ""
			},
			wantErr: true,
		},
		{
			name: "non-string field type is rejected",
			mutate: func(m map[string]interface{}) {
				m["submissionId"] = 1001 // int, not string - Redis always delivers strings
			},
			wantErr: true,
		},
		{
			name: "non-numeric integer field is rejected",
			mutate: func(m map[string]interface{}) {
				m["problemId"] = "not-a-number"
			},
			wantErr: true,
		},
		{
			name: "non-numeric float field is rejected",
			mutate: func(m map[string]interface{}) {
				m["timeLimitSeconds"] = "not-a-number"
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := make(map[string]interface{}, len(valid))
			for k, v := range valid {
				input[k] = v
			}
			tt.mutate(input)

			msg, err := ParseSubmissionMessage(input)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none (msg=%+v)", msg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if msg.SubmissionID != 1001 {
				t.Errorf("SubmissionID = %d, want 1001", msg.SubmissionID)
			}
			if msg.TimeLimitSeconds != 2.5 {
				t.Errorf("TimeLimitSeconds = %v, want 2.5", msg.TimeLimitSeconds)
			}
			if msg.Language != "JAVA" {
				t.Errorf("Language = %q, want JAVA", msg.Language)
			}
		})
	}
}
