package test

import (
	"context"
	"strings"
	"testing"
	"time"

	"nimda-judge-provisioner/internal/aws"
	"nimda-judge-provisioner/internal/config"
)

// TestGetFile downloads a single object from S3. Live-S3 integration test: it
// skips (rather than fails) when credentials or the object are absent.
//
// Uses the real object challenges/web-basic-01/web-basic-01.zip.
func TestGetFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("설정 읽기 실패 %v", err)
	}
	if cfg.AWSAccessKeyID == "" || cfg.AWSSecretAccessKey == "" {
		t.Skip("AWS 자격증명이 없어 S3 통합 테스트를 건너뜁니다")
	}
	if cfg.S3BucketName == "" {
		t.Fatalf("버킷 설정 로드 실패")
	}

	store, err := aws.NewS3Storage(ctx, cfg)
	if err != nil {
		t.Fatalf("버킷 생성에 실패했습니다. %v", err)
	}
	if err := store.PingBucket(ctx); err != nil {
		t.Fatalf("버킷 연결에 실패했습니다. %v", err)
	}

	const code = "web-basic-01"
	objectKey := "challenges/" + code + "/" + code + ".zip"

	output, err := store.GetFile(ctx, objectKey)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchKey") || strings.Contains(err.Error(), "StatusCode: 404") {
			t.Skipf("샘플 객체(%s)가 아직 없어 건너뜁니다", objectKey)
		}
		t.Fatalf("파일 읽기에 실패했습니다. %v", err)
	}
	if len(output) == 0 {
		t.Fatal("다운로드한 파일이 비어 있습니다")
	}
	t.Logf("파일 읽기 성공: %s (%d bytes)", objectKey, len(output))
}
