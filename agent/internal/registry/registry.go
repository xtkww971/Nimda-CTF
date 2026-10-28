// uuid와 컨테이너의 관계를 관리하는 클래스
// todo
//   - 나중에 떠 있는 인스턴스를 세어 메모리 과사용을 다룬다.
//   - PER_USER expiresAt 저장, 키 TTL은 ADR-0014 작업에서 정한다.
package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	protocol "nimda-ctf-protocol"
)

const (
	KeyPrefix       = "uuid:"
	SharedKeyPrefix = "shared:"
	LockKeyPrefix   = "lock:shared:"
	// pwn 포트 풀. 아직 안 나간 포트 번호들을 담는 Set이다.
	PortsFreeKey = "ctf:ports:free"
)

// hash 필드 이름
const (
	fieldIsolation     = "isolation"
	fieldInstanceID    = "instanceId"
	fieldChallengeCode = "challengeCode"
	fieldExpiresAt     = "expiresAt"
	fieldCreatedAt     = "createdAt"
	fieldIdleSince     = "idleSince"
	fieldPort          = "port" // TCP(pwn)일 때만 있다. 반납할 때 어느 포트였는지 읽는다
)

type Registry struct {
	rdb *redis.Client
}

func New(rdb *redis.Client) *Registry {
	return &Registry{rdb: rdb}
}

// ── uuid: 참가 정보 ─────────────────────────────────────────────────────────
// PER_USER인 경우 InstanceID로 해당 유저가 전에 컨테이너를 만든 적이 있는지 확인
// SHARED인 경우 ChallengeCode로 해당 문제를 제공하는 컨테이너가 있는지 확인
type Entry struct {
	Isolation     protocol.IsolationType
	InstanceID    string
	ChallengeCode string
	ExpiresAt     time.Time // 없으면 zero value
	Port          int       // TCP(pwn)일 때 밖에 연 포트. HTTP면 0
}

// PER_USER 컨테이너를 저장한다. 같은 uuid로 다시 부르면 덮어쓴다.
//
// port는 TCP(pwn)일 때 밖에 연 포트다. HTTP(web)는 포트를 안 받으므로 0을 넘긴다.
// 0이면 port 필드를 저장하지 않는다. "포트 없음"과 "0번 포트"를 구분할 필요가 없고,
// 30000-30199 풀에서 0이 나올 일도 없기 때문이다.
func (r *Registry) Save(ctx context.Context, uuid, instanceID string, port int) error {
	fields := []interface{}{
		fieldIsolation, string(protocol.IsolationPerUser),
		fieldInstanceID, instanceID,
	}
	if port != 0 {
		fields = append(fields, fieldPort, strconv.Itoa(port))
	}
	if err := r.rdb.HSet(ctx, key(uuid), fields...).Err(); err != nil {
		return fmt.Errorf("인스턴스 기록 실패 (uuid=%s): %w", uuid, err)
	}
	return nil
}

// 이 uuid의 참가 정보. 기록이 없으면 nil을 반환한다.
func (r *Registry) Entry(ctx context.Context, uuid string) (*Entry, error) {
	m, err := r.rdb.HGetAll(ctx, key(uuid)).Result()
	if err != nil {
		return nil, fmt.Errorf("인스턴스 조회 실패 (uuid=%s): %w", uuid, err)
	}
	if len(m) == 0 {
		return nil, nil
	}
	e := &Entry{
		Isolation:     protocol.IsolationType(m[fieldIsolation]),
		InstanceID:    m[fieldInstanceID],
		ChallengeCode: m[fieldChallengeCode],
	}
	if raw := m[fieldExpiresAt]; raw != "" {
		e.ExpiresAt, err = parseUnix(raw)
		if err != nil {
			return nil, fmt.Errorf("만료 시각 해석 실패 (uuid=%s): %w", uuid, err)
		}
	}
	if raw := m[fieldPort]; raw != "" {
		e.Port, err = strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("포트 해석 실패 (uuid=%s): %w", uuid, err)
		}
	}
	return e, nil
}

// PER_USER로 만든 컨테이너 ID. 기록이 없거나 SHARED면 빈 문자열을 반환한다.
// SHARED는 uuid가 컨테이너를 직접 가리키지 않기 때문에 Entry로 challengeCode를 읽어야 한다.
func (r *Registry) InstanceID(ctx context.Context, uuid string) (string, error) {
	e, err := r.Entry(ctx, uuid)
	if err != nil || e == nil {
		return "", err
	}
	return e.InstanceID, nil
}

// 컨테이너 삭제 이후 정보 동기화
func (r *Registry) Delete(ctx context.Context, uuid string) error {
	if err := r.rdb.Del(ctx, key(uuid)).Err(); err != nil {
		return fmt.Errorf("인스턴스 기록 삭제 실패 (uuid=%s): %w", uuid, err)
	}
	return nil
}

// ── shared: 공유 컨테이너 정보 ──────────────────────────────────────────────

type SharedInstance struct {
	InstanceID string
	CreatedAt  time.Time
	IdleSince  time.Time // lease가 0개가 된 시각. 사용 중이면 zero value
}

// 이 문제의 공유 컨테이너. 없으면 nil을 반환한다.
func (r *Registry) Shared(ctx context.Context, code string) (*SharedInstance, error) {
	m, err := r.rdb.HGetAll(ctx, sharedKey(code)).Result()
	if err != nil {
		return nil, fmt.Errorf("공유 인스턴스 조회 실패 (code=%s): %w", code, err)
	}
	if len(m) == 0 {
		return nil, nil
	}
	s := &SharedInstance{InstanceID: m[fieldInstanceID]}
	if raw := m[fieldCreatedAt]; raw != "" {
		if s.CreatedAt, err = parseUnix(raw); err != nil {
			return nil, fmt.Errorf("생성 시각 해석 실패 (code=%s): %w", code, err)
		}
	}
	if raw := m[fieldIdleSince]; raw != "" {
		if s.IdleSince, err = parseUnix(raw); err != nil {
			return nil, fmt.Errorf("idle 시각 해석 실패 (code=%s): %w", code, err)
		}
	}
	return s, nil
}

// 공유 컨테이너를 저장한다. 컨테이너를 새로 만들었으면 instanceId를 교체한다.
// 새 컨테이너라 idleSince는 지운다.
func (r *Registry) SaveShared(ctx context.Context, code, instanceID string) error {
	k := sharedKey(code)
	pipe := r.rdb.TxPipeline()
	pipe.HSet(ctx, k,
		fieldInstanceID, instanceID,
		fieldCreatedAt, formatUnix(time.Now()),
	)
	pipe.HDel(ctx, k, fieldIdleSince)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("공유 인스턴스 기록 실패 (code=%s): %w", code, err)
	}
	return nil
}

// 공유 컨테이너 정보를 지운다. 컨테이너를 내린 뒤 부른다(reaper).
func (r *Registry) DeleteShared(ctx context.Context, code string) error {
	if err := r.rdb.Del(ctx, sharedKey(code)).Err(); err != nil {
		return fmt.Errorf("공유 인스턴스 기록 삭제 실패 (code=%s): %w", code, err)
	}
	return nil
}

// ── lease ──────────────────────────────────────────────────────────────────

// 참가자를 공유 컨테이너에 붙인다. lease를 추가하고 uuid: 참가 정보를 저장한다.
//
// 반환값은 실제로 적용된 만료 시각이다.
// Redis Stream은 at-least-once라 같은 요청이 다시 올 수 있는데, 이때 lease가 연장되면 안 된다.
// 그래서 ZADD NX로 이미 있으면 건드리지 않고, 저장돼 있던 만료 시각을 그대로 돌려준다.
func (r *Registry) Join(ctx context.Context, code, uuid string, expiresAt time.Time) (time.Time, error) {
	lk := leasesKey(code)
	score := float64(expiresAt.Unix())

	if err := r.rdb.ZAddNX(ctx, lk, redis.Z{Score: score, Member: uuid}).Err(); err != nil {
		return time.Time{}, fmt.Errorf("lease 추가 실패 (code=%s, uuid=%s): %w", code, uuid, err)
	}
	// NX라서 이미 있었다면 예전 값이 남아 있다. 실제 값을 다시 읽는다.
	actual, err := r.rdb.ZScore(ctx, lk, uuid).Result()
	if err != nil {
		return time.Time{}, fmt.Errorf("lease 조회 실패 (code=%s, uuid=%s): %w", code, uuid, err)
	}
	applied := time.Unix(int64(actual), 0).UTC()

	pipe := r.rdb.TxPipeline()
	pipe.HSet(ctx, key(uuid),
		fieldIsolation, string(protocol.IsolationShared),
		fieldChallengeCode, code,
		fieldExpiresAt, formatUnix(applied),
	)
	// 참가자가 생겼으니 idle 상태가 아니다.
	pipe.HDel(ctx, sharedKey(code), fieldIdleSince)
	if _, err := pipe.Exec(ctx); err != nil {
		return time.Time{}, fmt.Errorf("참가 기록 실패 (code=%s, uuid=%s): %w", code, uuid, err)
	}
	return applied, nil
}

// 참가자의 lease를 반납한다. 컨테이너는 건드리지 않는다.
func (r *Registry) Leave(ctx context.Context, code, uuid string) error {
	pipe := r.rdb.TxPipeline()
	pipe.ZRem(ctx, leasesKey(code), uuid)
	pipe.Del(ctx, key(uuid))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("lease 반납 실패 (code=%s, uuid=%s): %w", code, uuid, err)
	}
	return nil
}

// 지금 남아 있는 lease 개수. 만료된 lease도 reaper가 지우기 전까지는 센다.
func (r *Registry) LeaseCount(ctx context.Context, code string) (int64, error) {
	n, err := r.rdb.ZCard(ctx, leasesKey(code)).Result()
	if err != nil {
		return 0, fmt.Errorf("lease 개수 조회 실패 (code=%s): %w", code, err)
	}
	return n, nil
}

// ── lock ───────────────────────────────────────────────────────────────────

// 문제 하나에 대한 lock을 잡는다. 이미 누가 잡고 있으면 ok=false.
//
// token은 Unlock 때 다시 넘겨야 한다.
// lock이 ttl로 먼저 풀리고 다른 쪽이 새로 잡은 뒤에, 원래 주인이 Unlock해서
// 남의 lock을 지우는 걸 막기 위해서다.
func (r *Registry) Lock(ctx context.Context, code string, ttl time.Duration) (token string, ok bool, err error) {
	token, err = newToken()
	if err != nil {
		return "", false, err
	}
	ok, err = r.rdb.SetNX(ctx, lockKey(code), token, ttl).Result()
	if err != nil {
		return "", false, fmt.Errorf("lock 획득 실패 (code=%s): %w", code, err)
	}
	return token, ok, nil
}

// 자기가 잡은 lock일 때만 지운다.
var unlockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)

func (r *Registry) Unlock(ctx context.Context, code, token string) error {
	if err := unlockScript.Run(ctx, r.rdb, []string{lockKey(code)}, token).Err(); err != nil {
		return fmt.Errorf("lock 해제 실패 (code=%s): %w", code, err)
	}
	return nil
}

// ── 전체 조회 (모니터링, Feature-01) ─────────────────────────────────────────
// 동아리 규모(동시 10~20개)라 키가 수십 개다. SCAN으로 훑어도 부담이 없다.

type Lease struct {
	UUID      string
	ExpiresAt time.Time
}

// uuid: 전체. key는 uuid.
func (r *Registry) AllEntries(ctx context.Context) (map[string]*Entry, error) {
	keys, err := r.scan(ctx, KeyPrefix+"*")
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Entry, len(keys))
	for _, k := range keys {
		uuid := strings.TrimPrefix(k, KeyPrefix)
		e, err := r.Entry(ctx, uuid)
		if err != nil {
			return nil, err
		}
		if e != nil { // SCAN과 읽기 사이에 지워졌을 수 있다
			out[uuid] = e
		}
	}
	return out, nil
}

// shared: 전체. key는 challengeCode.
func (r *Registry) AllShared(ctx context.Context) (map[string]*SharedInstance, error) {
	keys, err := r.scan(ctx, SharedKeyPrefix+"*")
	if err != nil {
		return nil, err
	}
	out := make(map[string]*SharedInstance, len(keys))
	for _, k := range keys {
		// shared:{code}:leases도 같은 접두사라 걸러낸다
		if strings.HasSuffix(k, ":leases") {
			continue
		}
		code := strings.TrimPrefix(k, SharedKeyPrefix)
		s, err := r.Shared(ctx, code)
		if err != nil {
			return nil, err
		}
		if s != nil {
			out[code] = s
		}
	}
	return out, nil
}

// 이 문제의 lease 전체. 만료 시각 순서다.
func (r *Registry) Leases(ctx context.Context, code string) ([]Lease, error) {
	zs, err := r.rdb.ZRangeWithScores(ctx, leasesKey(code), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("lease 목록 조회 실패 (code=%s): %w", code, err)
	}
	out := make([]Lease, 0, len(zs))
	for _, z := range zs {
		uuid, _ := z.Member.(string)
		out = append(out, Lease{UUID: uuid, ExpiresAt: time.Unix(int64(z.Score), 0).UTC()})
	}
	return out, nil
}

func (r *Registry) scan(ctx context.Context, pattern string) ([]string, error) {
	var keys []string
	iter := r.rdb.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("registry SCAN 실패 (pattern=%s): %w", pattern, err)
	}
	return keys, nil
}

// ── 포트 풀 (pwn/TCP) ────────────────────────────────────────────────────────
// 풀은 "아직 안 나간 포트"를 담는 Set 하나다(ADR-0013, ADR-0020).
// "누가 어느 포트를 쓰나"는 컨테이너 라벨 nimda.ctf.port로 알 수 있어서 따로 저장하지 않는다.

// 포트를 하나 꺼낸다. SPOP은 원자적이라 두 요청이 같은 포트를 받는 일이 없다.
// 풀이 비면 ok=false다. 부르는 쪽은 이때 "지금 자리가 없다"로 FAILED를 돌려준다.
func (r *Registry) AllocatePort(ctx context.Context) (port int, ok bool, err error) {
	raw, err := r.rdb.SPop(ctx, PortsFreeKey).Result()
	if err == redis.Nil {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("포트 할당 실패: %w", err)
	}
	port, err = strconv.Atoi(raw)
	if err != nil {
		// 풀에 숫자가 아닌 값이 들어가 있다. 그 값은 버린다(다시 넣지 않는다).
		return 0, false, fmt.Errorf("풀에 숫자가 아닌 포트가 있습니다 (%q): %w", raw, err)
	}
	return port, true, nil
}

// 포트를 풀에 되돌린다. Set이라 같은 포트를 두 번 넣어도 하나만 남는다.
func (r *Registry) ReleasePort(ctx context.Context, port int) error {
	if err := r.rdb.SAdd(ctx, PortsFreeKey, strconv.Itoa(port)).Err(); err != nil {
		return fmt.Errorf("포트 반납 실패 (port=%d): %w", port, err)
	}
	return nil
}

// 풀을 다시 계산한다. free = {lo..hi} - used.
//
// used는 지금 떠 있는 컨테이너가 쓰는 포트다(nimda.ctf.port 라벨). 도커를 보는 일이라
// 호출부(runtime)가 모아서 넘긴다. 여기서는 숫자 집합만 받는다.
//
// 반납을 못 하고 죽으면 포트가 풀에서 사라진 채로 남는다(ADR-0013). 실제로 떠 있는
// 컨테이너를 기준으로 다시 계산하면 redis 값이 어떻게 틀어져 있든 맞춰진다.
// agent가 시작할 때 한 번 부른다.
func (r *Registry) InitPortPool(ctx context.Context, lo, hi int, used []int) error {
	taken := make(map[int]bool, len(used))
	for _, p := range used {
		taken[p] = true
	}

	free := make([]interface{}, 0, hi-lo+1)
	for p := lo; p <= hi; p++ {
		if !taken[p] {
			free = append(free, strconv.Itoa(p))
		}
	}

	// 기존 풀을 통째로 갈아끼운다. 지우고 다시 넣는 사이에 할당이 끼면 그 포트를 잃지만,
	// 시작 시 한 번만 부르고 그때는 CREATE를 아직 안 받으므로 끼어들 할당이 없다.
	pipe := r.rdb.TxPipeline()
	pipe.Del(ctx, PortsFreeKey)
	if len(free) > 0 {
		pipe.SAdd(ctx, PortsFreeKey, free...)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("포트 풀 초기화 실패 (%d-%d): %w", lo, hi, err)
	}
	return nil
}

// ── helpers ────────────────────────────────────────────────────────────────

func key(uuid string) string       { return KeyPrefix + uuid }
func sharedKey(code string) string { return SharedKeyPrefix + code }
func leasesKey(code string) string { return SharedKeyPrefix + code + ":leases" }
func lockKey(code string) string   { return LockKeyPrefix + code }

// 시각은 unix 초로 저장한다. lease score와 단위를 맞추기 위해서다.
func formatUnix(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func parseUnix(s string) (time.Time, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(n, 0).UTC(), nil
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("lock token 생성 실패: %w", err)
	}
	return hex.EncodeToString(b), nil
}
