package aws

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	appConfig "nimda-judge-provisioner/internal/config"
)

// 실제 S3에 붙어 presigned URL을 발급하고, 그 URL로 정말 받아지는지까지 확인한다.
// 자격증명이 없으면 건너뛴다 (파이프라인 테스트와 같은 규칙).
//
// 대상 객체는 파이프라인 테스트가 쓰는 것과 같은, 버킷에 실제로 올라가 있는 문제 zip이다.
const presignTestKey = "challenges/web-basic-01/web-basic-01.zip"

func newTestStorage(t *testing.T) *S3Storage {
	t.Helper()
	cfg, err := appConfig.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	if cfg.AWSAccessKeyID == "" || cfg.AWSSecretAccessKey == "" {
		t.Skip("AWS 자격증명이 없어 presign 테스트를 건너뜁니다")
	}
	store, err := NewS3Storage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("S3 client: %v", err)
	}
	return store
}

func TestPresignGetObjectIsDownloadable(t *testing.T) {
	store := newTestStorage(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	url, expiresAt, err := store.PresignGetObject(ctx, presignTestKey, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	if url == "" {
		t.Fatal("presigned URL이 비어 있음")
	}
	if d := time.Until(expiresAt); d <= 0 || d > 6*time.Minute {
		t.Errorf("만료 시각이 이상함: %s (남은 시간 %s)", expiresAt, d)
	}

	// 서명만 맞는 게 아니라 실제로 내려받아지는지 본다 (본문은 안 읽고 헤더만).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("요청 생성: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("네트워크로 S3에 못 붙어 확인을 건너뜁니다: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("presigned URL GET = %d, want 200", resp.StatusCode)
	}
	if resp.ContentLength == 0 {
		t.Error("내려받은 본문이 비어 있음")
	}
}

func TestPresignMissingObjectReturnsNotFound(t *testing.T) {
	store := newTestStorage(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, _, err := store.PresignGetObject(ctx, "challenges/존재하지-않는-문제/nope.zip", time.Minute)
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("없는 키에 대해 ErrObjectNotFound여야 함, got %v", err)
	}
}
