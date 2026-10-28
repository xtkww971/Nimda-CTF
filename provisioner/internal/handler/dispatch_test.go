package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	protocol "nimda-ctf-protocol"

	"nimda-judge-provisioner/internal/config"
	"nimda-judge-provisioner/internal/queue"
)

// TCP+SHARED 거부는 다운로드·전달 전에 걸러야 한다. 아래 두 fake는 "그 뒤 단계가
// 불렸는가"만 본다. 불렸다면 거부가 늦게 일어난 것이다.
type recordingDownloader struct{ called bool }

func (r *recordingDownloader) GetFile(context.Context, string) ([]byte, error) {
	r.called = true
	return nil, errors.New("GetFile은 불리면 안 된다")
}

type recordingAgent struct{ called bool }

func (r *recordingAgent) Dispatch(context.Context, protocol.RequestMessage) error {
	r.called = true
	return nil
}

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

// pwn(TCP)을 SHARED로 내면 한 명이 셸을 따서 모두의 문제를 망친다.
// 다운로드·전달 전에 FAILED로 끝나야 하고, 사유가 웹에 돌아가야 한다.
func TestHandlerRejectsTCPShared(t *testing.T) {
	dl := &recordingDownloader{}
	ag := &recordingAgent{}
	h := NewHandler(dl, ag, config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	res, err := h.Handle(context.Background(), &CreateInstanceMessage{
		UUID:          "u-tcp-shared",
		ChallengeCode: "pwn-bof",
		AccessType:    protocol.AccessTCP,
		IsolationType: string(protocol.IsolationShared),
		FlagType:      protocol.FlagStatic,
	})
	if err != nil {
		t.Fatalf("거부는 에러가 아니라 FAILED 결과여야 함: %v", err)
	}
	if res == nil || res.Status != queue.StatusFailed {
		t.Fatalf("status = %+v, want FAILED", res)
	}
	if res.UUID != "u-tcp-shared" {
		t.Errorf("UUID = %q, 웹이 자기 요청을 찾을 수 있어야 한다", res.UUID)
	}
	if dl.called {
		t.Error("거부한 요청인데 S3 다운로드를 시도함")
	}
	if ag.called {
		t.Error("거부한 요청인데 agent로 전달함")
	}
}
