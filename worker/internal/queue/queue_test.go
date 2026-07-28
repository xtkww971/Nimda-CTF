// 파일 경로: queue/queue_test.go
package queue

import (
	"fmt"
	"nimda-judge-worker/internal/config"
	"os"
	"os/signal"
	"syscall"
	"testing"

	"github.com/redis/go-redis/v9"
)

// TestRedisQueueDaemon은 브루노 테스트 등을 위해 큐를 상시 구동 상태로 띄워둡니다.
func TestRedisQueueDaemon(t *testing.T) {
	cfg, err := config.Load()
	client := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})

	if err != nil {
		fmt.Printf("설정 읽기 실패")
	}

	streamName := cfg.StreamKey()
	groupName := "grading_group_1"
	consumerName := cfg.ConsumerName()

	// 1. 큐 실행 (백그라운드에서 계속 레디스를 폴링함)
	q := NewRedisQueue(client)
	q.Start(streamName, groupName, consumerName)

	// 2. 워커 고루틴 실행 (채널에 데이터가 들어오면 바로바로 처리)
	go func() {
		t.Log("👷 [Worker] 채점 대기 중... (브루노에서 스트림으로 데이터를 쏴보세요!)")
		for msg := range q.Channel {
			t.Logf("✅ [작업 수신 완료] 문제 번호: %d ID: %d, 언어: %s, 코드: %s\n", msg.ProblemID,
				msg.SubmissionID, msg.Language, msg.SourceCode)
		}
		t.Log("👷 [Worker] 큐 채널이 닫혔습니다.")
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	t.Log("🚀 큐 서버가 실행되었습니다. 종료하려면 터미널에서 Ctrl+C를 누르세요.")
	<-sigChan // 여기서 블로킹되며 큐가 계속 살아있음

	t.Log("🛑 종료 신호 감지. 큐 서버를 안전하게 종료합니다...")
	q.Stop()
	q.Wait()
	t.Log("✨ 큐 서버가 안전하게 종료되었습니다.")
}
