package aws

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (s *S3Storage) GetFile(ctx context.Context, objectKey string) ([]byte, error) {
	input := &s3.GetObjectInput{
		Bucket: &s.BucketName,
		Key:    &objectKey,
	}

	output, err := s.Client.GetObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("S3에서 파일 [%s] 가져오기 실패: %w", objectKey, err)
	}

	defer output.Body.Close()

	fileData, err := io.ReadAll(output.Body)

	if err != nil {
		return nil, fmt.Errorf("파일[%s] 읽어오기 실패 %w", objectKey, err)
	}

	return fileData, nil
}
