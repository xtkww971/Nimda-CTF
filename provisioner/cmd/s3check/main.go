// Provisioner가 S3 버킷을 읽을 수 있는지 확인하는 모듈
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"nimda-judge-provisioner/internal/aws"
	"nimda-judge-provisioner/internal/config"
)

// checkTimeout은 점검 전체에 거는 제한 시간이다.
const checkTimeout = 10 * time.Second

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		fail("Cannot read config: %v", err)
	}

	check, err := aws.CheckBucket(ctx, cfg)

	// 키 정보들은 채워져있는지만 확인한다.
	fmt.Println("[s3check] 설정")
	fmt.Printf("  region        : %s\n", check.Region)
	fmt.Printf("  bucket        : %s\n", check.Bucket)
	fmt.Printf("  access key    : %s\n", filled(check.HasAccessKey))
	fmt.Printf("  secret key    : %s\n", filled(check.HasSecretKey))

	if err != nil {
		fail("%v", err)
	}

	fmt.Printf("[s3check] 버킷 [%s] 연결 및 접근 권한 확인 완료\n", check.Bucket)
}

func filled(ok bool) string {
	if !ok {
		return "(비어 있음)"
	}
	return "설정됨"
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[s3check] "+format+"\n", args...)
	os.Exit(1)
}
