package test

import (
	"context"
	"testing"
	"time"

	"nimda-judge-provisioner/internal/aws"
	"nimda-judge-provisioner/internal/config"
)

// TestS3PingBucket checks that we can reach the configured S3 bucket with the
// credentials in .env. Live-S3 integration test: skips without credentials.
func TestS3PingBucket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Config 로드 실패 %v", err)
	}
	if cfg.AWSAccessKeyID == "" || cfg.AWSSecretAccessKey == "" {
		t.Skip("AWS 자격증명이 없어 S3 통합 테스트를 건너뜁니다")
	}
	if cfg.S3BucketName == "" {
		t.Fatalf("버킷 설정 로드 실패")
	}

	store, err := aws.NewS3Storage(ctx, cfg)
	if err != nil {
		t.Fatalf("버킷을 가지고 오지 못했습니다. %v", err)
	}
	if err := store.PingBucket(ctx); err != nil {
		t.Fatalf("S3 버킷 연결 테스트 실패 %v", err)
	}

	t.Logf("버킷 [%s] 연결에 성공했습니다.", cfg.S3BucketName)
}
