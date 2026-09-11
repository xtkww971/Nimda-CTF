// 조율자가 에이전트에게 일을 넘기는 파이프라인 (Redis Stream).
package agentclient

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

	protocol "nimda-ctf-protocol"
)

// 에이전트에게 작업을 넘기는 쪽. 핸들러는 이 인터페이스만 알면 된다.
type Dispatcher interface {
	// 인스턴스 생성을 에이전트에게 맡긴다. 반환된 에러는 "스트림에 넣지도 못했다"는 뜻이다.
	// 빌드/실행 실패는 여기가 아니라 결과 스트림에 Status=FAILED로 온다.
	// 인스턴스 생성 요청
	DispatchCreate(ctx context.Context, req protocol.CreateRequest) error

	// 인스턴스 파괴 요청
	DispatchDestroy(ctx context.Context, req protocol.DestroyRequest) error
}

// Redis Stream으로 구현한 Dispatcher.
type StreamDispatcher struct {
	rdb    *redis.Client
	stream string
	log    *slog.Logger
}

func NewStreamDispatcher(rdb *redis.Client, stream string, log *slog.Logger) *StreamDispatcher {
	return &StreamDispatcher{rdb: rdb, stream: stream, log: log}
}

var _ Dispatcher = (*StreamDispatcher)(nil)

func (d *StreamDispatcher) DispatchCreate(ctx context.Context, req protocol.CreateRequest) error {
	return d.publish(ctx, req.ToStreamValues(), req.UUID, protocol.ActionCreate)
}

func (d *StreamDispatcher) DispatchDestroy(ctx context.Context, req protocol.DestroyRequest) error {
	return d.publish(ctx, req.ToStreamValues(), req.UUID, protocol.ActionDestroy)
}

// 스트림에 남겨둘 엔트리 수의 대략적 상한.
//
// Redis Stream은 ACK해도 엔트리가 지워지지 않는다 — 안 자르면 요청을 넣는 만큼 무한정
// 자란다. 전용 redis를 100MB로 잡아뒀으므로 여기서 잘라주지 않으면 며칠 안에 찬다.
//
// 이 숫자만큼 밀려 있다는 건 에이전트가 사실상 멈춰 있다는 뜻이라, 여기 걸려 잘리는
// 상황이면 이미 트리밍이 아니라 그쪽이 문제다. 정확히 자르는 것보다 싸므로 근사(~)로 자른다.
const streamMaxLen = 10000

func (d *StreamDispatcher) publish(ctx context.Context, values map[string]interface{}, uuid, action string) error {
	if err := d.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: d.stream,
		Values: values,
		MaxLen: streamMaxLen,
		Approx: true,
	}).Err(); err != nil {
		// 넣지 못했으면 에이전트는 이 요청의 존재 자체를 모른다. 호출한 쪽이 ACK하지 않고
		// 남겨야 웹 요청이 유실되지 않는다.
		return fmt.Errorf("agent 스트림(%s) 발행 실패: %w", d.stream, err)
	}
	d.log.Info("dispatched to agent", "stream", d.stream, "action", action, "uuid", uuid)
	return nil
}
