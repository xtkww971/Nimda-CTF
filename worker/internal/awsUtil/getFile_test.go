package awsUtil

import (
	"context"
	appConfig "nimda-judge-worker/internal/config"
	"testing"
	"time"
)

func TestGetFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	defer cancel()

	cfg, err := appConfig.Load()

	if err != nil {
		t.Fatalf("설정 읽기 실패 %v", err)
	}

	if cfg.S3BucketName == "" {
		t.Fatalf("버킷 설정 로드 실패")
	}

	S3Storage, err := NewS3Storage(ctx, cfg)

	if err != nil {
		t.Fatalf("버킷 생성에 실패했습니다. %v", err)
	}

	if S3Storage.PingBucket(ctx) != nil {
		t.Fatalf("버킷 연결에 실패했습니다. %v", err)
	}

	// 객체 키를 어떻게 가지고 와야 하는가?
	path := "problems/1/1.in"
	output, err := S3Storage.GetFile(ctx, path)

	if err != nil {
		t.Fatalf("파일 읽기에 실패했습니다. %v", err)
	}

	t.Logf("파일 읽기에 성공했습니다. \n%s", output)
}
