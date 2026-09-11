package aws

import (
	"context"
	"fmt"

	appConfig "nimda-judge-provisioner/internal/config"
)

// 자격증명과 버킷 권한이 맞는지만 확인
type BucketCheck struct {
	Region       string
	Bucket       string
	HasAccessKey bool
	HasSecretKey bool
}

func CheckBucket(ctx context.Context, cfg appConfig.Config) (BucketCheck, error) {
	check := BucketCheck{
		Region:       cfg.AWSRegion,
		Bucket:       cfg.S3BucketName,
		HasAccessKey: cfg.AWSAccessKeyID != "",
		HasSecretKey: cfg.AWSSecretAccessKey != "",
	}

	// 설정에 버킷 이름이 없는 경우
	if check.Bucket == "" {
		return check, fmt.Errorf("버킷 이름이 비어 있습니다 (AWS_S3_BUCKET)")
	}

	store, err := NewS3Storage(ctx, cfg)
	if err != nil {
		return check, fmt.Errorf("S3 클라이언트를 만들지 못했습니다: %w", err)
	}

	if err := store.PingBucket(ctx); err != nil {
		return check, fmt.Errorf("버킷에 접근하지 못했습니다: %w", err)
	}

	return check, nil
}
