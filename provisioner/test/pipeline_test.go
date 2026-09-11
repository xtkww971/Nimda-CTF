package test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	protocol "nimda-ctf-protocol"

	"nimda-judge-provisioner/internal/agentclient"
	"nimda-judge-provisioner/internal/aws"
	"nimda-judge-provisioner/internal/config"
	"nimda-judge-provisioner/internal/handler"
	"nimda-judge-provisioner/internal/queue"
)

// TestPipelinePublishesResult drives the whole Provisioner pipeline against a
// real Redis and real S3: a create request on the web queue triggers an S3
// download of challenges/web-basic-01/web-basic-01.zip, an unzip, a publish to
// the agent ops stream, and - once a fake Agent answers on the agent results
// stream - a result on the web result stream.
//
// 소켓 시절과 달리 한 함수 안에서 끝나지 않는다. 조율자는 요청을 넘기고 손을 떼고,
// 결과는 별도 컨슈머가 받아 옮긴다(ADR-0012). 그래서 이 테스트는 두 컨슈머를 다 띄우고
// 그 사이를 가짜 에이전트가 잇는다.
//
// The Agent here is a fake that records the build context and answers with a
// canned READY - the real Docker build/run/reachability is covered by the agent
// module's own test. This keeps the pipeline test fast and free of Docker.
func TestPipelinePublishesResult(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6380"})
	pingCtx, cancelPing := context.WithTimeout(context.Background(), time.Second)
	defer cancelPing()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		t.Skipf("test Redis at localhost:6380 not reachable (%v)", err)
	}

	suffix := itoa(time.Now().UnixNano())
	reqStream := "test:ctf:request:ops:" + suffix
	resultStream := "test:ctf:request:results:" + suffix
	agentOpsStream := "test:ctf:agent:ops:" + suffix
	agentResultStream := "test:ctf:agent:results:" + suffix

	t.Setenv("REDIS_ADDR", "localhost:6380")
	t.Setenv("CTF_QUEUE_STREAM_KEY", reqStream)
	t.Setenv("CTF_QUEUE_RESULT_STREAM_KEY", resultStream)
	t.Setenv("CTF_QUEUE_CONSUMER_GROUP", "test-prov-"+suffix)
	t.Setenv("CTF_QUEUE_AGENT_STREAM_KEY", agentOpsStream)
	t.Setenv("CTF_QUEUE_AGENT_RESULT_STREAM_KEY", agentResultStream)
	t.Setenv("CTF_QUEUE_AGENT_CONSUMER_GROUP", "test-agents-"+suffix)
	t.Setenv("CTF_QUEUE_BLOCK_TIMEOUT", "200ms")
	t.Setenv("CONSUMER_NAME", "pipeline-test")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	if cfg.AWSAccessKeyID == "" || cfg.AWSSecretAccessKey == "" {
		t.Skip("AWS 자격증명이 없어 파이프라인 테스트를 건너뜁니다")
	}
	cfg.BuildWorkDir = t.TempDir()

	t.Cleanup(func() {
		bg := context.Background()
		rdb.Del(bg, reqStream, resultStream, agentOpsStream, agentResultStream)
		_ = rdb.Close()
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	s3, err := aws.NewS3Storage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("S3 client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 웹 요청 컨슈머: S3에서 받아 풀고 에이전트 스트림에 넣는다. 결과는 안 기다린다.
	dispatcher := agentclient.NewStreamDispatcher(rdb, cfg.AgentStreamKey(), logger)
	router := handler.NewRouter(
		handler.NewTaskDispatcher(
			handler.NewHandler(s3, dispatcher, cfg, logger),
			handler.NewDownloadHandler(s3, cfg, logger),
			logger,
		),
		logger,
	)
	consumer := queue.NewConsumer(rdb, cfg, router, logger)
	if err := consumer.EnsureGroup(ctx); err != nil {
		t.Fatalf("ensure group: %v", err)
	}
	go consumer.Run(ctx)

	// 에이전트 결과 컨슈머: 에이전트 결과를 웹 결과 스트림으로 옮긴다.
	// 운영에서는 읽는 통(ctf-redis)과 쓰는 통(nimda-redis)이 다르지만, 여기서는 테스트용
	// redis 하나로 양쪽을 다 본다 — 옮기는 로직 자체는 통이 몇 개든 같다.
	agentResults := queue.NewAgentResultConsumer(rdb, rdb, cfg, handler.NewAgentResultMapper(logger), logger)
	if err := agentResults.EnsureGroup(ctx); err != nil {
		t.Fatalf("ensure agent result group: %v", err)
	}
	go agentResults.Run(ctx)

	// 가짜 에이전트: ops 스트림을 읽고, 받은 빌드 경로를 기록한 뒤 결과를 돌려준다.
	gotPath := startFakeAgent(t, ctx, rdb, agentOpsStream, agentResultStream)

	// Publish a create request for a challenge that really exists in S3.
	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: reqStream,
		Values: map[string]interface{}{
			"uuid":              "uuid-pipeline-1",
			"challengeCode":     "web-basic-01",
			"challengeCategory": "WEB",
			"isolationType":     "PER_USER",
			"flagType":          "STATIC",
		},
	}).Err(); err != nil {
		t.Fatalf("publish request: %v", err)
	}

	// The Agent should have received a build-context PATH that really exists and
	// holds a Dockerfile - proving the Provisioner downloaded + unzipped the
	// real S3 object and passed the folder (no tar over the stream).
	select {
	case path := <-gotPath:
		if path == "" {
			t.Fatal("BuildContextPath가 비어있음")
		}
		if !dirHasFile(path, "Dockerfile") {
			t.Errorf("빌드 폴더(%s)에 Dockerfile이 없음 (다운로드/압축해제 확인 필요)", path)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Agent가 30초 안에 빌드 요청을 못 받음")
	}

	// And a READY result should land on the web result stream.
	values := waitForResult(t, rdb, resultStream, 10*time.Second)
	if values["uuid"] != "uuid-pipeline-1" {
		t.Errorf("uuid = %v", values["uuid"])
	}
	if values["status"] != protocol.StatusReady {
		t.Errorf("status = %v, want READY", values["status"])
	}
	if values["host"] != "10.9.8.7" || values["port"] != "54321" {
		t.Errorf("result가 Agent 응답을 그대로 전달하지 않음: %+v", values)
	}
	// 결과에 남는 식별자는 uuid 하나다. 누가 어떤 문제를 요청했는지는 웹이 자기 쪽에서
	// uuid로 되짚는다.
	if values["challengeCode"] != nil || values["userId"] != nil {
		t.Errorf("결과에 challengeCode/userId가 실림: %+v", values)
	}
	if values["expiresAt"] == nil {
		t.Errorf("PER_USER 결과에 expiresAt이 있어야 함: %+v", values)
	}
	t.Logf("파이프라인 결과: %+v", values)
}

// ── fake Agent (ops 스트림을 읽고 canned READY를 결과 스트림에 넣는다) ─────────

// 컨슈머 그룹 없이 XREAD로 꼬리부터 읽는다. 진짜 에이전트는 그룹을 쓰지만, 여기서
// 확인하려는 건 "조율자가 무엇을 넣었나"와 "결과가 웹까지 오나"뿐이다.
func startFakeAgent(t *testing.T, ctx context.Context, rdb *redis.Client, opsStream, resultStream string) <-chan string {
	t.Helper()
	gotPath := make(chan string, 1)

	go func() {
		lastID := "$"
		for {
			if ctx.Err() != nil {
				return
			}
			streams, err := rdb.XRead(ctx, &redis.XReadArgs{
				Streams: []string{opsStream, lastID},
				Count:   1,
				Block:   200 * time.Millisecond,
			}).Result()
			if err != nil {
				continue // redis.Nil(타임아웃) 포함. ctx가 끝나면 위에서 빠져나간다.
			}
			for _, stream := range streams {
				for _, msg := range stream.Messages {
					lastID = msg.ID
					req, err := protocol.ParseRequestMessage(msg.Values)
					if err != nil {
						t.Errorf("에이전트가 받은 생성 요청을 해석하지 못함: %v (%+v)", err, msg.Values)
						continue
					}
					select {
					case gotPath <- req.BuildContextPath:
					default:
					}

					result := protocol.ResponseMessage{
						UUID:       req.UUID,
						Action:     protocol.ActionCreate,
						Status:     protocol.StatusReady,
						InstanceID: "fake-instance",
						Host:       "10.9.8.7",
						Port:       54321,
						ExpiresAt:  "2026-08-17T11:00:00Z",
					}
					if err := rdb.XAdd(ctx, &redis.XAddArgs{
						Stream: resultStream,
						Values: result.ToStreamValues(),
					}).Err(); err != nil {
						t.Errorf("가짜 에이전트가 결과를 발행하지 못함: %v", err)
					}
				}
			}
		}
	}()

	return gotPath
}

// dirHasFile reports whether a file named `name` exists anywhere under dir.
func dirHasFile(dir, name string) bool {
	found := false
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == name {
			found = true
		}
		return nil
	})
	return found
}
