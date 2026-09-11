// uuid - 컨테이너 id를 저장
// 조율자와 에이전트가 같이 보는 redis(ctf-redis)에 위치한다.

// todo 이후 구현 예정 작업
//   - 같은 요청이 다시 배달돼도 이미 만든 것을 돌려준다(Redis Stream은 at-least-once다).
//   - DESTROY 요청이 uuid만 들고 와도 어느 컨테이너인지 찾는다.
//   - 나중에 떠 있는 인스턴스를 세어 메모리 과사용을 다룬다.
//
// TTL은 아직 걸지 않는다. 수명은 웹이 보내는 요청에 TTL이 생기면 그때 정한다.
package registry

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// 키 접두사. uuid:{uuid} -> 컨테이너 ID.
const KeyPrefix = "uuid:"

type Registry struct {
	rdb *redis.Client
}

func New(rdb *redis.Client) *Registry {
	return &Registry{rdb: rdb}
}

// uuid가 어느 컨테이너가 됐는지 기록한다. 같은 uuid로 다시 부르면 덮어쓴다.
func (r *Registry) Save(ctx context.Context, uuid, instanceID string) error {
	if err := r.rdb.Set(ctx, key(uuid), instanceID, 0).Err(); err != nil {
		return fmt.Errorf("인스턴스 기록 실패 (uuid=%s): %w", uuid, err)
	}
	return nil
}

// 이 uuid로 만든 컨테이너 ID. 기록이 없으면 빈 문자열을 반환
func (r *Registry) InstanceID(ctx context.Context, uuid string) (string, error) {
	id, err := r.rdb.Get(ctx, key(uuid)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("인스턴스 조회 실패 (uuid=%s): %w", uuid, err)
	}
	return id, nil
}

// 컨테이너 삭제 이후 정보 동기화
func (r *Registry) Delete(ctx context.Context, uuid string) error {
	if err := r.rdb.Del(ctx, key(uuid)).Err(); err != nil {
		return fmt.Errorf("인스턴스 기록 삭제 실패 (uuid=%s): %w", uuid, err)
	}
	return nil
}

func key(uuid string) string {
	return KeyPrefix + uuid
}
