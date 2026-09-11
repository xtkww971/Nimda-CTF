package test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"nimda-judge-provisioner/internal/aws"
	"nimda-judge-provisioner/internal/config"
	"nimda-judge-provisioner/internal/handler"
	"nimda-judge-provisioner/internal/queue"
)

// TestDownloadPipelinePublishesPresignedURL은 첨부파일 다운로드 경로를 끝까지 태운다:
// 웹 백엔드(Spring)가 보내는 것과 같은 모양의 요청을 요청 스트림에 넣고 → 조율자가 실제 S3에
// 서명해 → 결과 스트림에 presigned URL이 실리고 → 그 URL로 정말 파일이 받아지는지까지 본다.
//
// 실행 조건은 파이프라인 테스트와 같다 (테스트 Redis 6380 + AWS 자격증명). 없으면 건너뛴다.
func TestDownloadPipelinePublishesPresignedURL(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6380"})
	pingCtx, cancelPing := context.WithTimeout(context.Background(), time.Second)
	defer cancelPing()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		t.Skipf("test Redis at localhost:6380 not reachable (%v)", err)
	}

	suffix := time.Now().UnixNano()
	reqStream := "test:ctf:request:ops:dl:" + itoa(suffix)
	resultStream := "test:ctf:request:results:dl:" + itoa(suffix)

	t.Setenv("REDIS_ADDR", "localhost:6380")
	t.Setenv("CTF_QUEUE_STREAM_KEY", reqStream)
	t.Setenv("CTF_QUEUE_RESULT_STREAM_KEY", resultStream)
	t.Setenv("CTF_QUEUE_CONSUMER_GROUP", "test-dl-"+itoa(suffix))
	t.Setenv("CTF_QUEUE_BLOCK_TIMEOUT", "200ms")
	t.Setenv("CONSUMER_NAME", "download-test")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	if cfg.AWSAccessKeyID == "" || cfg.AWSSecretAccessKey == "" {
		t.Skip("AWS 자격증명이 없어 다운로드 테스트를 건너뜁니다")
	}

	t.Cleanup(func() {
		bg := context.Background()
		rdb.Del(bg, reqStream, resultStream)
		_ = rdb.Close()
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	s3, err := aws.NewS3Storage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("S3 client: %v", err)
	}

	// 컨슈머가 직접 아는 건 Router뿐이다. 무엇을 할지는 Router가 유형을 보고 정한다.
	router := handler.NewRouter(
		handler.NewTaskDispatcher(nil, handler.NewDownloadHandler(s3, cfg, logger), logger),
		logger,
	)
	consumer := queue.NewConsumer(rdb, cfg, router, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := consumer.EnsureGroup(ctx); err != nil {
		t.Fatalf("ensure group: %v", err)
	}
	go consumer.Run(ctx)

	// 웹 백엔드가 XADD하는 필드 그대로 (통합된 요청 메시지).
	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: reqStream,
		Values: map[string]interface{}{
			"uuid":              "uuid-download-1",
			"challengeCode":     "web-basic-01",
			"challengeCategory": "FORENSIC",
			"isolationType":     "SHARED",
			"flagType":          "STATIC",
		},
	}).Err(); err != nil {
		t.Fatalf("publish request: %v", err)
	}

	values := waitForResult(t, rdb, resultStream, 15*time.Second)
	if values["uuid"] != "uuid-download-1" {
		t.Errorf("uuid = %v", values["uuid"])
	}
	if values["status"] != queue.StatusReady {
		t.Fatalf("status = %v (%v), want READY", values["status"], values["message"])
	}
	if values["expiresAt"] == nil {
		t.Errorf("READY 결과에 expiresAt이 있어야 함: %+v", values)
	}

	url, _ := values["downloadUrl"].(string)
	if !strings.Contains(url, "X-Amz-Signature") {
		t.Fatalf("결과의 url이 서명된 링크가 아님: %q", url)
	}

	// 백엔드가 사용자에게 그대로 넘길 링크다. 실제로 내려받아지는지 확인한다.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("요청 생성: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("네트워크로 S3에 못 붙어 확인을 건너뜁니다: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("presigned URL GET = %d, want 200", resp.StatusCode)
	}
}

// 문제 코드가 경로를 벗어나는 요청은 서명 없이 FAILED 결과로만 돌아와야 한다
// (버킷 전체가 열리는 것 방지).
//
// 예전에는 웹이 objectKey를 실어 보낼 수 있어서 그 키를 검사했다. 지금은 메시지에서 뺐고
// 키를 challengeCode에서 유도하므로, 막아야 할 입력은 문제 코드 하나다.
func TestDownloadPipelineRejectsUnsafeChallengeCode(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6380"})
	pingCtx, cancelPing := context.WithTimeout(context.Background(), time.Second)
	defer cancelPing()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		t.Skipf("test Redis at localhost:6380 not reachable (%v)", err)
	}

	suffix := time.Now().UnixNano()
	reqStream := "test:ctf:request:ops:reject:" + itoa(suffix)
	resultStream := "test:ctf:request:results:reject:" + itoa(suffix)

	t.Setenv("REDIS_ADDR", "localhost:6380")
	t.Setenv("CTF_QUEUE_STREAM_KEY", reqStream)
	t.Setenv("CTF_QUEUE_RESULT_STREAM_KEY", resultStream)
	t.Setenv("CTF_QUEUE_CONSUMER_GROUP", "test-dl-reject-"+itoa(suffix))
	t.Setenv("CTF_QUEUE_BLOCK_TIMEOUT", "200ms")
	t.Setenv("CONSUMER_NAME", "download-reject-test")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		rdb.Del(bg, reqStream, resultStream)
		_ = rdb.Close()
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// S3에 붙을 필요조차 없다. 키 검사에서 걸러져야 하므로 서명을 시도하면 그 자체가 실패다.
	router := handler.NewRouter(
		handler.NewTaskDispatcher(nil, handler.NewDownloadHandler(refusingPresigner{t: t}, cfg, logger), logger),
		logger,
	)
	consumer := queue.NewConsumer(rdb, cfg, router, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := consumer.EnsureGroup(ctx); err != nil {
		t.Fatalf("ensure group: %v", err)
	}
	go consumer.Run(ctx)

	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: reqStream,
		Values: map[string]interface{}{
			"uuid":              "uuid-download-evil",
			"challengeCode":     "../boards/누군가의-첨부파일",
			"challengeCategory": "FORENSIC",
			"isolationType":     "SHARED",
			"flagType":          "STATIC",
		},
	}).Err(); err != nil {
		t.Fatalf("publish request: %v", err)
	}

	values := waitForResult(t, rdb, resultStream, 10*time.Second)
	if values["status"] != queue.StatusFailed {
		t.Errorf("status = %v, want FAILED", values["status"])
	}
	if values["downloadUrl"] != nil {
		t.Errorf("거절한 요청에 downloadUrl이 실림: %+v", values)
	}
	if values["message"] == nil {
		t.Errorf("FAILED 결과에 사유가 있어야 함: %+v", values)
	}
}

// 서명을 시도하면 테스트를 실패시키는 가짜 presigner.
type refusingPresigner struct{ t *testing.T }

func (p refusingPresigner) PresignGetObject(_ context.Context, objectKey string, _ time.Duration) (string, time.Time, error) {
	p.t.Errorf("허용되지 않는 키인데 서명을 시도함: %q", objectKey)
	return "", time.Time{}, nil
}
