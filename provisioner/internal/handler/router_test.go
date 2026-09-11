package handler

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"nimda-judge-provisioner/internal/queue"
)

// Router가 무슨 작업으로 분류해 무엇을 넘겼는지만 기록하는 가짜 핸들러.
//
// Router는 이제 분류만 하고 분기는 TaskDispatcher가 하므로, 여기서 볼 것은
// "어떤 TaskMessage가 만들어졌는가" 하나다.
type spyTask struct {
	got TaskMessage
}

func (s *spyTask) Handle(_ context.Context, msg TaskMessage) (*queue.Result, error) {
	s.got = msg
	return &queue.Result{UUID: msg.ID()}, nil
}

func request(category string) *queue.Request {
	return &queue.Request{
		UUID:              "uuid-1",
		ChallengeCode:     "web-basic-01",
		ChallengeCategory: category,
		IsolationType:     "PER_USER",
		FlagType:          "STATIC",
	}
}

func newRouterWithSpy() (*Router, *spyTask) {
	spy := &spyTask{}
	r := NewRouter(spy, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return r, spy
}

// 유형별로 어느 쪽이 맡는지.
func TestRouterSendsInstanceCategoriesToAgent(t *testing.T) {
	tests := []struct {
		category    string
		wantToAgent bool
	}{
		// 참가자가 살아 있는 서비스에 붙어야 푸는 유형.
		{"WEB", true},
		{"PWN", true},

		// 웹 백엔드가 소문자나 공백을 섞어 보내도 같게 봐야 한다.
		{"web", true},
		{" WEB ", true},
		{"pwn", true},

		// 문제 파일만 내려받으면 되는 유형.
		{"REVERSING", false},
		{"FORENSICS", false},
		{"CRYPTO", false},
		{"MISC", false},

		// 모르는 유형과 빈 값은 첨부파일 쪽으로 떨어진다. 인스턴스는 "필요하다고 아는
		// 것만" 띄운다 — 모르는 유형을 에이전트로 보내면 빌드할 것도 없는 요청으로
		// 컨테이너를 올리려 든다.
		{"UNKNOWN", false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.category, func(t *testing.T) {
			r, spy := newRouterWithSpy()

			if _, err := r.Handle(context.Background(), request(tc.category)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			want := TaskGetPresignedURL
			if tc.wantToAgent {
				want = TaskCreateInstance
			}
			if spy.got == nil {
				t.Fatal("Router가 작업을 넘기지 않음")
			}
			if spy.got.Kind() != want {
				t.Errorf("task = %q, want %q", spy.got.Kind(), want)
			}
		})
	}
}

// 분배만 맞고 메시지가 비어 있으면 소용이 없다.
// 작업별 메시지는 이미 파싱된 queue.Request에서 옮겨 담으므로, 필드가 새면 여기서 걸린다.
func TestRouterFillsCreateInstanceMessage(t *testing.T) {
	r, spy := newRouterWithSpy()

	if _, err := r.Handle(context.Background(), request("WEB")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := spy.got.(*CreateInstanceMessage)
	if !ok {
		t.Fatalf("WEB 요청인데 *CreateInstanceMessage가 아님: %T", spy.got)
	}
	if got.UUID != "uuid-1" {
		t.Errorf("UUID = %q, want %q", got.UUID, "uuid-1")
	}
	if got.ChallengeCode != "web-basic-01" {
		t.Errorf("ChallengeCode = %q, want %q", got.ChallengeCode, "web-basic-01")
	}
	if got.ChallengeCategory != "WEB" {
		t.Errorf("ChallengeCategory = %q, want %q", got.ChallengeCategory, "WEB")
	}
	if got.IsolationType != "PER_USER" {
		t.Errorf("IsolationType = %q, want %q", got.IsolationType, "PER_USER")
	}
	if got.FlagType != "STATIC" {
		t.Errorf("FlagType = %q, want %q", got.FlagType, "STATIC")
	}
}

func TestRouterFillsGetPresignedURL(t *testing.T) {
	r, spy := newRouterWithSpy()

	if _, err := r.Handle(context.Background(), request("FORENSICS")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := spy.got.(*GetPresignedURL)
	if !ok {
		t.Fatalf("FORENSICS 요청인데 *GetPresignedURL이 아님: %T", spy.got)
	}
	if got.UUID != "uuid-1" {
		t.Errorf("UUID = %q, want %q", got.UUID, "uuid-1")
	}
	if got.ChallengeCode != "web-basic-01" {
		t.Errorf("ChallengeCode = %q, want %q", got.ChallengeCode, "web-basic-01")
	}
	if got.ChallengeCategory != "FORENSICS" {
		t.Errorf("ChallengeCategory = %q, want %q", got.ChallengeCategory, "FORENSICS")
	}
}

// classify는 Router가 쓰는 판단 자체다. 유형이 늘어날 때 여기부터 깨져야 한다.
func TestClassify(t *testing.T) {
	tests := []struct {
		category string
		want     TaskKind
	}{
		{"WEB", TaskCreateInstance},
		{"PWN", TaskCreateInstance},
		{"REVERSING", TaskGetPresignedURL},
		{"FORENSICS", TaskGetPresignedURL},
		{"CRYPTO", TaskGetPresignedURL},
		{"MISC", TaskGetPresignedURL},
		{"", TaskGetPresignedURL},
	}

	for _, tc := range tests {
		if got := classify(tc.category); got != tc.want {
			t.Errorf("classify(%q) = %q, want %q", tc.category, got, tc.want)
		}
	}
}
