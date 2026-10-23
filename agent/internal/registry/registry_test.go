package registry

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	protocol "nimda-ctf-protocol"
)

// 실제 Redis가 필요하다. provisioner 테스트와 같은 테스트 Redis(localhost:6380)를 쓴다.
//
//	docker run -d --rm --name ctf-redis-test -p 6380:6379 redis:7-alpine
func newTestRegistry(t *testing.T) (*Registry, *redis.Client) {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6380"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("test Redis at localhost:6380 not reachable (%v)", err)
	}
	t.Cleanup(func() { rdb.Close() })
	return New(rdb), rdb
}

// 테스트끼리 키가 겹치지 않게 이름을 만든다.
func unique(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func cleanup(t *testing.T, rdb *redis.Client, keys ...string) {
	t.Cleanup(func() { rdb.Del(context.Background(), keys...) })
}

// PER_USER는 기존 Save/InstanceID/Delete 그대로 동작해야 한다. dispatcher가 이걸 쓰고 있다.
func TestPerUserSaveAndLookup(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	uuid := unique("u")
	cleanup(t, rdb, key(uuid))

	if err := r.Save(ctx, uuid, "container-1"); err != nil {
		t.Fatalf("save: %v", err)
	}
	id, err := r.InstanceID(ctx, uuid)
	if err != nil || id != "container-1" {
		t.Fatalf("InstanceID = %q, %v", id, err)
	}
	e, err := r.Entry(ctx, uuid)
	if err != nil || e == nil || e.Isolation != protocol.IsolationPerUser {
		t.Fatalf("Entry = %+v, %v", e, err)
	}

	if err := r.Delete(ctx, uuid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if id, _ := r.InstanceID(ctx, uuid); id != "" {
		t.Errorf("삭제 후에도 InstanceID = %q", id)
	}
}

func TestEntryMissingReturnsNil(t *testing.T) {
	r, _ := newTestRegistry(t)
	e, err := r.Entry(context.Background(), unique("missing"))
	if err != nil || e != nil {
		t.Fatalf("Entry = %+v, %v (기록이 없으면 nil, nil)", e, err)
	}
}

func TestSharedSaveAndLookup(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	code := unique("web")
	cleanup(t, rdb, sharedKey(code))

	if s, err := r.Shared(ctx, code); err != nil || s != nil {
		t.Fatalf("처음엔 없어야 함: %+v, %v", s, err)
	}
	if err := r.SaveShared(ctx, code, "shared-1"); err != nil {
		t.Fatalf("save shared: %v", err)
	}
	s, err := r.Shared(ctx, code)
	if err != nil || s == nil || s.InstanceID != "shared-1" || s.CreatedAt.IsZero() {
		t.Fatalf("Shared = %+v, %v", s, err)
	}

	// 컨테이너를 다시 만들면 instanceId가 바뀐다.
	if err := r.SaveShared(ctx, code, "shared-2"); err != nil {
		t.Fatalf("save shared again: %v", err)
	}
	if s, _ := r.Shared(ctx, code); s.InstanceID != "shared-2" {
		t.Errorf("instanceId = %q, want shared-2", s.InstanceID)
	}

	if err := r.DeleteShared(ctx, code); err != nil {
		t.Fatalf("delete shared: %v", err)
	}
	if s, _ := r.Shared(ctx, code); s != nil {
		t.Errorf("삭제 후에도 남아 있음: %+v", s)
	}
}

// 두 참가자가 같은 문제에 들어오면 lease가 두 개, uuid:는 각자 challengeCode를 가리킨다.
func TestJoinTwoParticipants(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	code, a, b := unique("web"), unique("a"), unique("b")
	cleanup(t, rdb, sharedKey(code), leasesKey(code), key(a), key(b))

	exp := time.Now().Add(time.Hour).Truncate(time.Second).UTC()
	for _, u := range []string{a, b} {
		applied, err := r.Join(ctx, code, u, exp)
		if err != nil {
			t.Fatalf("join %s: %v", u, err)
		}
		if !applied.Equal(exp) {
			t.Errorf("applied = %s, want %s", applied, exp)
		}
	}

	if n, _ := r.LeaseCount(ctx, code); n != 2 {
		t.Errorf("LeaseCount = %d, want 2", n)
	}
	e, err := r.Entry(ctx, a)
	if err != nil || e == nil {
		t.Fatalf("Entry(a) = %+v, %v", e, err)
	}
	if e.Isolation != protocol.IsolationShared || e.ChallengeCode != code || !e.ExpiresAt.Equal(exp) {
		t.Errorf("Entry(a) = %+v", e)
	}
	// SHARED uuid는 컨테이너를 직접 가리키지 않는다.
	if id, _ := r.InstanceID(ctx, a); id != "" {
		t.Errorf("SHARED uuid의 InstanceID = %q, want empty", id)
	}
}

// 같은 요청이 redelivery되면 lease가 연장되면 안 된다.
func TestJoinRedeliveryDoesNotExtendLease(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	code, u := unique("web"), unique("u")
	cleanup(t, rdb, sharedKey(code), leasesKey(code), key(u))

	first := time.Now().Add(time.Hour).Truncate(time.Second).UTC()
	if _, err := r.Join(ctx, code, u, first); err != nil {
		t.Fatalf("join: %v", err)
	}
	applied, err := r.Join(ctx, code, u, first.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("join again: %v", err)
	}
	if !applied.Equal(first) {
		t.Errorf("redelivery로 lease가 바뀜: %s, want %s", applied, first)
	}
	if e, _ := r.Entry(ctx, u); !e.ExpiresAt.Equal(first) {
		t.Errorf("uuid: expiresAt = %s, want %s", e.ExpiresAt, first)
	}
	if n, _ := r.LeaseCount(ctx, code); n != 1 {
		t.Errorf("LeaseCount = %d, want 1", n)
	}
}

// 참가자가 들어오면 idle 상태가 풀린다.
func TestJoinClearsIdleSince(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	code, u := unique("web"), unique("u")
	cleanup(t, rdb, sharedKey(code), leasesKey(code), key(u))

	if err := r.SaveShared(ctx, code, "shared-1"); err != nil {
		t.Fatalf("save shared: %v", err)
	}
	rdb.HSet(ctx, sharedKey(code), fieldIdleSince, formatUnix(time.Now())) // reaper가 찍었다고 가정

	if _, err := r.Join(ctx, code, u, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("join: %v", err)
	}
	if s, _ := r.Shared(ctx, code); !s.IdleSince.IsZero() {
		t.Errorf("Join 후에도 idleSince가 남아 있음: %s", s.IdleSince)
	}
}

// 한 명이 나가도 컨테이너 정보와 다른 사람의 lease는 남는다.
func TestLeaveKeepsOthers(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	code, a, b := unique("web"), unique("a"), unique("b")
	cleanup(t, rdb, sharedKey(code), leasesKey(code), key(a), key(b))

	if err := r.SaveShared(ctx, code, "shared-1"); err != nil {
		t.Fatalf("save shared: %v", err)
	}
	exp := time.Now().Add(time.Hour)
	r.Join(ctx, code, a, exp)
	r.Join(ctx, code, b, exp)

	if err := r.Leave(ctx, code, a); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if e, _ := r.Entry(ctx, a); e != nil {
		t.Errorf("a의 uuid:가 남아 있음: %+v", e)
	}
	if e, _ := r.Entry(ctx, b); e == nil {
		t.Error("b의 uuid:가 사라짐")
	}
	if n, _ := r.LeaseCount(ctx, code); n != 1 {
		t.Errorf("LeaseCount = %d, want 1", n)
	}
	if s, _ := r.Shared(ctx, code); s == nil {
		t.Error("공유 컨테이너 정보가 사라짐")
	}
}

func TestLockIsExclusive(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	code := unique("web")
	cleanup(t, rdb, lockKey(code))

	token, ok, err := r.Lock(ctx, code, time.Minute)
	if err != nil || !ok {
		t.Fatalf("첫 lock 실패: ok=%v, %v", ok, err)
	}
	if _, ok, _ := r.Lock(ctx, code, time.Minute); ok {
		t.Fatal("이미 잡힌 lock을 또 잡음")
	}
	if err := r.Unlock(ctx, code, token); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if _, ok, _ := r.Lock(ctx, code, time.Minute); !ok {
		t.Error("unlock 후에도 lock을 못 잡음")
	}
}

// lock이 TTL로 풀린 뒤 다른 쪽이 잡았으면, 원래 주인의 Unlock이 그 lock을 지우면 안 된다.
func TestUnlockWithStaleTokenKeepsNewOwner(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	code := unique("web")
	cleanup(t, rdb, lockKey(code))

	stale, ok, _ := r.Lock(ctx, code, 50*time.Millisecond)
	if !ok {
		t.Fatal("첫 lock 실패")
	}
	time.Sleep(100 * time.Millisecond) // TTL 만료

	if _, ok, _ := r.Lock(ctx, code, time.Minute); !ok {
		t.Fatal("만료 후 새 lock 실패")
	}
	if err := r.Unlock(ctx, code, stale); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if _, ok, _ := r.Lock(ctx, code, time.Minute); ok {
		t.Error("예전 token으로 새 주인의 lock이 지워짐")
	}
}

// 모니터링용 전체 조회. shared:{code}:leases가 AllShared에 섞여 들어오면 안 된다.
func TestAllEntriesSharedAndLeases(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	code, p, s := unique("web"), unique("p"), unique("s")
	cleanup(t, rdb, key(p), key(s), sharedKey(code), leasesKey(code))

	r.Save(ctx, p, "per-user-container")
	r.SaveShared(ctx, code, "shared-container")
	exp := time.Now().Add(time.Hour).Truncate(time.Second).UTC()
	r.Join(ctx, code, s, exp)

	entries, err := r.AllEntries(ctx)
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}
	if e := entries[p]; e == nil || e.Isolation != protocol.IsolationPerUser || e.InstanceID != "per-user-container" {
		t.Errorf("PER_USER entry = %+v", e)
	}
	if e := entries[s]; e == nil || e.Isolation != protocol.IsolationShared || e.ChallengeCode != code {
		t.Errorf("SHARED entry = %+v", e)
	}

	shared, err := r.AllShared(ctx)
	if err != nil {
		t.Fatalf("AllShared: %v", err)
	}
	if sh := shared[code]; sh == nil || sh.InstanceID != "shared-container" {
		t.Errorf("shared = %+v", sh)
	}
	if _, ok := shared[code+":leases"]; ok {
		t.Error("leases 키가 공유 컨테이너로 잡힘")
	}

	leases, err := r.Leases(ctx, code)
	if err != nil || len(leases) != 1 || leases[0].UUID != s || !leases[0].ExpiresAt.Equal(exp) {
		t.Errorf("Leases = %+v, %v", leases, err)
	}
}
