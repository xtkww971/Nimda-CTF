package awsUtil

import (
	"context"
	appConfig "nimda-judge-provisioner/internal/config"
	"testing"
	"time"
)

func TestLoadS3(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg, err := appConfig.Load()

	if err != nil {
		t.Fatalf("Config 로드 실패 %v", err)
	}

	if cfg.S3BucketName == "" {
		t.Fatalf("버킷 설정 로드 실패")
	}

	s3Store, err := NewS3Storage(ctx, cfg)
	if err != nil {
		t.Fatalf("버킷을 가지고 오지 못했습니다. %v", err)
	}

	err = s3Store.PingBucket(ctx)
	if err != nil {
		t.Fatalf("S3 버킷 연결 테스트 실패 %v", err)
	}

	t.Logf("버킷 [%s] 연결에 성공했습니다.", cfg.S3BucketName)
}
