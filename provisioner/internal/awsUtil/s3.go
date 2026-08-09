package awsUtil

import (
	"context"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	appConfig "nimda-judge-provisioner/internal/config"
)

type S3Storage struct {
	Client     *s3.Client
	BucketName string
}

// 생성자
func NewS3Storage(ctx context.Context, cfg appConfig.Config) (*S3Storage, error) {
	var optionFns []func(*config.LoadOptions) error
	// optionFns - 설정 파일에 있는 리전 정보를 aws 설정에 주입하는 함수를 추가
	optionFns = append(optionFns, config.WithRegion(cfg.AWSRegion))

	// 접근 키, 비밀 키 존재하는지 확인(맞는지까지는 확인하지 않음)
	if cfg.AWSAccessKeyID != "" && cfg.AWSSecretAccessKey != "" {
		creds := credentials.NewStaticCredentialsProvider(
			cfg.AWSAccessKeyID,
			cfg.AWSSecretAccessKey,
			"",
		)
		// optionFns - 인증 정보를 aws 설정에 주입하는 함수를 추가
		optionFns = append(optionFns, config.WithCredentialsProvider(creds))
	} else {
		return nil, fmt.Errorf("키 정보가 들어가 있지 않습니다")
	}

	/*
		1. optionFns은 함수 배열이기에 ...으로 입력
		2. config.LoadDefaultConfig가 기본 설정을 불러와서 optionFns을 순환하면서 값을 주입
	*/
	awsCfg, err := config.LoadDefaultConfig(ctx, optionFns...)
	if err != nil {
		return nil, fmt.Errorf("AWS 설정 로드 실패: %w", err)
	}

	// 버킷과 상호작용하는 컴포넌트 반환 - 단! 인증 정보가 맞는지 여부는 확인하지 않음
	client := s3.NewFromConfig(awsCfg)

	return &S3Storage{
		Client:     client,
		BucketName: cfg.S3BucketName,
	}, nil
}

// S3 버킷에 접근 할 수 있는지 확인 - 인증 정보가 맞는지 확인하는 함수
func (s *S3Storage) PingBucket(ctx context.Context) error {
	_, err := s.Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: &s.BucketName,
	})
	if err != nil {
		return fmt.Errorf("S3 버킷 [%s] 연결 실패: %w", s.BucketName, err)
	}

	log.Printf("S3 버킷 [%s] 연결 및 권한 확인 성공", s.BucketName)
	return nil
}
