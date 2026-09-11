package aws

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// 객체가 없을 때 PresignGetObject/ObjectExists가 돌려주는 에러.
var ErrObjectNotFound = errors.New("S3 객체가 존재하지 않습니다")

func (s *S3Storage) PresignGetObject(ctx context.Context, objectKey string, ttl time.Duration) (string, time.Time, error) {
	// 객체가 존재하는지 확인
	if err := s.ensureObjectExists(ctx, objectKey); err != nil {
		return "", time.Time{}, err
	}

	presigner := s3.NewPresignClient(s.Client)
	req, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.BucketName,
		Key:    &objectKey,
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("presigned URL 발급 실패 [%s]: %w", objectKey, err)
	}

	return req.URL, time.Now().UTC().Add(ttl), nil
}

// 객체가 있으면 nil, 없으면 ErrObjectNotFound
func (s *S3Storage) ensureObjectExists(ctx context.Context, objectKey string) error {
	_, err := s.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.BucketName,
		Key:    &objectKey,
	})
	if err == nil {
		return nil
	}

	var notFound *types.NotFound
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &notFound) || errors.As(err, &noSuchKey) {
		return fmt.Errorf("%w: %s", ErrObjectNotFound, objectKey)
	}
	return fmt.Errorf("S3 객체 확인 실패 [%s]: %w", objectKey, err)
}
