package queue

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// 가장 먼저 데이터를 받는 채널
type RedisQueue struct {
	client *redis.Client
	// 채널을 외부에서도 접근할 수 있게 설정
	Channel chan SubmissionMessage

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// 생성자
func NewRedisQueue(client *redis.Client) *RedisQueue {
	ctx, cancel := context.WithCancel(context.Background())
	return &RedisQueue{
		client: client,
		// 일단 버퍼 크기를 50으로 설정했음 나중에 바꿀 필요 있어보임
		Channel: make(chan SubmissionMessage, 50),
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (r *RedisQueue) Start(streamName, groupName, consumerName string) {
	r.wg.Add(1)

	go func() {
		defer r.wg.Done()
		defer close(r.Channel)

		log.Println("채점 큐 수신(Redis Stream)을 시작합니다...")

		// 1. 컨슈머 그룹 생성 (스트림이 없으면 같이 생성)
		// 이미 그룹이 존재하면 "BUSYGROUP" 에러가 발생하므로 무시합니다.
		err := r.client.XGroupCreateMkStream(r.ctx, streamName, groupName, "0").Err()
		if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
			log.Printf("[경고] 컨슈머 그룹 생성 실패: %v", err)
			// 치명적 에러일 수 있으나, 일단 진행을 시도합니다.
		}

		for {
			select {
			case <-r.ctx.Done():
				log.Println("종료 신호 감지: 수신 고루틴을 안전하게 종료합니다.")
				return
			default:
			}

			// 2. XReadGroup으로 데이터 가져오기 (2초 타임아웃)
			args := &redis.XReadGroupArgs{
				Group:    groupName,
				Consumer: consumerName,
				Streams:  []string{streamName, ">"}, // ">": 다른 컨슈머가 읽지 않은 새 메시지만 가져옴
				Count:    1,                         // 한 번에 1개씩 처리 (필요에 따라 늘릴 수 있음)
				Block:    2 * time.Second,
			}

			streams, err := r.client.XReadGroup(r.ctx, args).Result()

			if err != nil {
				// BLPOP 때 만들어둔 handleRedisError를 그대로 재사용 가능합니다! (redis.Nil 처리 등)
				if r.handleRedisError(err) {
					return
				}
				continue
			}

			// 3. 데이터 파싱 및 처리
			if len(streams) > 0 && len(streams[0].Messages) > 0 {
				xmsg := streams[0].Messages[0] // Redis Stream 원본 메시지

				// 작성해주신 강력한 파서 호출
				msg, err := ParseSubmissionMessage(xmsg.Values)
				if err != nil {
					// 💡 독성 메시지(Poison Message) 처리
					// 파싱할 수 없는 메시지는 재시도해도 실패하므로, 에러 로깅 후 즉시 ACK 처리하여 스트림에서 치워버립니다.
					log.Printf("[Poison Message 감지] 파싱 실패 (MsgID: %s). 스킵 및 ACK 처리. 사유: %v", xmsg.ID, err)
					r.client.XAck(r.ctx, streamName, groupName, xmsg.ID)
					continue
				}

				// 4. 안전한 채널 전송
				select {
				case r.Channel <- *msg: // msg가 포인터이므로 역참조하여 전송 (q chan SubmissionMessage 기준)
					// 전송이 완료되면 Redis에 해당 메시지를 성공적으로 받았다고 ACK(승인)를 보냅니다.
					r.client.XAck(r.ctx, streamName, groupName, xmsg.ID)

				case <-r.ctx.Done():
					log.Println("채널 전송 대기 중 종료 신호 감지됨.")
					return
				}
			}
		}
	}()
}

func (r *RedisQueue) handleRedisError(err error) bool {
	// 1. 2초동안 데이터가 들어오지 않아서 재시도
	if err == redis.Nil {
		return false // 멈추지 않고 계속 진행 (continue)
	}

	// 2. 서버 종료 신호 (컨텍스트 취소)
	if err == context.Canceled {
		return true // 루프 완전히 탈출 (return)
	}

	log.Printf("[Redis 통신 에러]: %v", err)
	time.Sleep(1 * time.Second) // 대기 시간 갱신

	return false
}

func (r *RedisQueue) Stop() {
	r.cancel()
}

// Wait는 수신 고루틴이 찌꺼기 작업 없이 완전히 안전하게 종료될 때까지 대기합니다.
func (r *RedisQueue) Wait() {
	r.wg.Wait()
}
