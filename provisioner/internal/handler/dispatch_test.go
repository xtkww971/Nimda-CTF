package handler

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"nimda-judge-provisioner/internal/queue"
)

// TaskMessage를 구현하지만 TaskDispatcher가 모르는 작업.
// 작업 종류를 추가하면서 분기에 case를 안 붙인 상황을 흉내낸다.
type unknownTask struct{}

func (unknownTask) Kind() TaskKind { return TaskKind("SOMETHING_NEW") }
func (unknownTask) ID() string     { return "uuid-unknown" }

// 모르는 작업이 오면 조용히 넘어가면 안 된다.
//
// 타입 스위치는 case가 빠져도 컴파일러가 잡아주지 못하는 자리다. 여기서 nil을 돌려주면
// 컨슈머는 "발행할 결과가 없다"로 읽고 ACK해버려서, 요청이 아무 응답 없이 사라진다.
// 웹은 영원히 PENDING을 보게 된다.
func TestTaskDispatcherRejectsUnknownTask(t *testing.T) {
	d := NewTaskDispatcher(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	res, err := d.Handle(context.Background(), unknownTask{})
	if err != nil {
		t.Fatalf("모르는 작업은 재시도 대상이 아니라 FAILED 결과여야 함: %v", err)
	}
	if res == nil {
		t.Fatal("결과가 nil이면 요청이 응답 없이 사라진다")
	}
	if res.Status != queue.StatusFailed {
		t.Errorf("Status = %q, want %q", res.Status, queue.StatusFailed)
	}
	if res.UUID != "uuid-unknown" {
		t.Errorf("UUID = %q, 웹이 자기 요청을 찾을 수 있어야 한다", res.UUID)
	}
	// 어느 작업에서 막혔는지 알 수 있어야 한다.
	if !strings.Contains(res.Message, "SOMETHING_NEW") {
		t.Errorf("message = %q, 작업 종류가 들어 있어야 함", res.Message)
	}
}

// 첨부파일 작업은 presign 쪽으로 가야 한다.
// (인스턴스 핸들러를 nil로 두었으므로, 잘못 갈라지면 nil 역참조로 터진다.)
func TestTaskDispatcherRoutesPresignTask(t *testing.T) {
	p := &fakePresigner{}
	d := NewTaskDispatcher(nil, newDownloadHandler(p), slog.New(slog.NewTextHandler(io.Discard, nil)))

	res, err := d.Handle(context.Background(), &GetPresignedURL{
		UUID:              "uuid-1",
		ChallengeCode:     "rev-01",
		ChallengeCategory: "REVERSING",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != queue.StatusReady {
		t.Fatalf("Status = %q (%s), want READY", res.Status, res.Message)
	}
	if p.gotKey != "challenges/rev-01/rev-01.zip" {
		t.Errorf("presign key = %q, 첨부파일 핸들러를 타지 않은 것으로 보임", p.gotKey)
	}
	if res.DownloadURL == "" {
		t.Errorf("READY인데 downloadUrl이 비어 있음: %+v", res)
	}
}
