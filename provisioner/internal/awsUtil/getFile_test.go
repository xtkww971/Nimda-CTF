package awsUtil

import (
	"context"
	appConfig "nimda-judge-provisioner/internal/config"
	"strings"
	"testing"
	"time"
)

// TestGetFile is a live-S3 integration test: it needs real credentials and a
// seeded object. It skips (rather than fails) when those aren't present, so a
// plain `go test ./...` stays green without AWS access or test data.
//
// TODO(CTF): the sample key below is a challenges/ path; seed a real object
// (e.g. via the challenge upload API) and point this at it for a full check.
func TestGetFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg, err := appConfig.Load()
	if err != nil {
		t.Fatalf("설정 읽기 실패 %v", err)
	}
	if cfg.AWSAccessKeyID == "" || cfg.AWSSecretAccessKey == "" {
		t.Skip("AWS 자격증명이 없어 S3 통합 테스트를 건너뜁니다")
	}
	if cfg.S3BucketName == "" {
		t.Fatalf("버킷 설정 로드 실패")
	}

	s3Storage, err := NewS3Storage(ctx, cfg)
	if err != nil {
		t.Fatalf("버킷 생성에 실패했습니다. %v", err)
	}
	if err := s3Storage.PingBucket(ctx); err != nil {
		t.Fatalf("버킷 연결에 실패했습니다. %v", err)
	}

	path := "challenges/web-01/web-01.zip"
	output, err := s3Storage.GetFile(ctx, path)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchKey") || strings.Contains(err.Error(), "StatusCode: 404") {
			t.Skipf("샘플 객체(%s)가 아직 없어 건너뜁니다", path)
		}
		t.Fatalf("파일 읽기에 실패했습니다. %v", err)
	}

	t.Logf("파일 읽기에 성공했습니다. \n%s", output)
}
