package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"nimda-judge-provisioner/internal/aws"
	"nimda-judge-provisioner/internal/config"
	"nimda-judge-provisioner/internal/queue"
)

// 서명을 흉내내는 가짜 S3. 실제 서명은 SDK가 하니, 여기서는 "어떤 키로 몇 초짜리를
// 요청했는지"와 에러 분기만 본다.
type fakePresigner struct {
	gotKey string
	gotTTL time.Duration
	err    error
}

func (f *fakePresigner) PresignGetObject(_ context.Context, objectKey string, ttl time.Duration) (string, time.Time, error) {
	f.gotKey = objectKey
	f.gotTTL = ttl
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	return "https://bucket.s3.amazonaws.com/" + objectKey + "?X-Amz-Signature=fake", time.Now().UTC().Add(ttl), nil
}

func newDownloadHandler(p *fakePresigner) *DownloadHandler {
	cfg := config.Config{PresignTTL: 5 * time.Minute}
	return NewDownloadHandler(p, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func downloadReq(code string) *GetPresignedURL {
	return &GetPresignedURL{
		UUID:              "uuid-dl-1",
		ChallengeCode:     code,
		ChallengeCategory: "FORENSICS",
	}
}

func TestDownloadPresignsDerivedKey(t *testing.T) {
	p := &fakePresigner{}
	res, err := newDownloadHandler(p).Handle(context.Background(), downloadReq("rev-01"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != queue.StatusReady {
		t.Fatalf("status = %q (%s), want READY", res.Status, res.Message)
	}
	// 키는 항상 문제 코드에서 유도한다. 메시지에는 objectKey가 없다.
	if p.gotKey != "challenges/rev-01/rev-01.zip" {
		t.Errorf("presign key = %q", p.gotKey)
	}
	if p.gotTTL != 5*time.Minute {
		t.Errorf("presign ttl = %s, want 5m", p.gotTTL)
	}
	if res.DownloadURL == "" || res.ExpiresAt == "" {
		t.Errorf("READY 결과에 downloadUrl/expiresAt이 있어야 함: %+v", res)
	}
	if res.UUID != "uuid-dl-1" {
		t.Errorf("결과가 요청의 uuid를 되돌려주지 않음: %+v", res)
	}
	// 첨부파일 결과에는 인스턴스 접속 정보가 있으면 안 된다.
	if res.Host != "" || res.Port != 0 {
		t.Errorf("다운로드 결과에 host/port가 실림: %+v", res)
	}
}

// 문제 코드가 경로를 벗어나면 서명 자체를 시도하지 않아야 한다.
// 보낸 쪽을 믿고 서명하면 그 값이 곧 버킷 전체 열람 권한이 된다.
func TestDownloadRejectsUnsafeChallengeCode(t *testing.T) {
	cases := []struct {
		name string
		code string
	}{
		{"경로 구분자", "../../boards"},
		{"백슬래시", "boards" + "\\" + "secret"},
		{"슬래시 포함", "challenges/secret"},
		{"빈 문제 코드", ""},
		{"상위 경로", ".."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakePresigner{}
			res, err := newDownloadHandler(p).Handle(context.Background(), downloadReq(tc.code))
			if err != nil {
				t.Fatalf("거절은 에러가 아니라 FAILED 결과여야 함: %v", err)
			}
			if res.Status != queue.StatusFailed {
				t.Errorf("status = %q, want FAILED (key=%q)", res.Status, p.gotKey)
			}
			if p.gotKey != "" {
				t.Errorf("거절한 요청인데 서명을 시도함: %q", p.gotKey)
			}
			if res.DownloadURL != "" {
				t.Errorf("FAILED 결과에 downloadUrl이 실림: %q", res.DownloadURL)
			}
		})
	}
}

func TestDownloadMissingObjectIsPermanentFailure(t *testing.T) {
	p := &fakePresigner{err: aws.ErrObjectNotFound}
	res, err := newDownloadHandler(p).Handle(context.Background(), downloadReq("rev-01"))
	if err != nil {
		t.Fatalf("없는 파일은 재시도 대상이 아니라 FAILED 결과여야 함: %v", err)
	}
	if res.Status != queue.StatusFailed {
		t.Errorf("status = %q, want FAILED", res.Status)
	}
	if !strings.Contains(res.Message, "첨부파일이 없습니다") {
		t.Errorf("message = %q", res.Message)
	}
}

func TestDownloadTransientErrorRetries(t *testing.T) {
	p := &fakePresigner{err: errors.New("dial tcp: i/o timeout")}
	res, err := newDownloadHandler(p).Handle(context.Background(), downloadReq("rev-01"))
	// 일시적 실패는 error로 올려야 컨슈머가 ACK 없이 두고 재시도한다.
	if err == nil {
		t.Fatalf("일시적 실패는 error로 올라와야 함, got result %+v", res)
	}
	if res != nil {
		t.Errorf("재시도 대상인데 결과를 발행하려 함: %+v", res)
	}
}
