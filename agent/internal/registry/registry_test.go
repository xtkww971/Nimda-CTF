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

	if err := r.Save(ctx, uuid, "container-1", 0, time.Time{}); err != nil {
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

	r.Save(ctx, p, "per-user-container", 0, time.Time{})
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

// ── 포트 풀 (ADR-0020) ───────────────────────────────────────────────────────

// InitPortPool은 떠 있는 포트를 뺀 나머지로 풀을 채운다.
func TestInitPortPoolExcludesUsed(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	cleanup(t, rdb, PortsFreeKey)

	if err := r.InitPortPool(ctx, 30000, 30002, []int{30001}); err != nil {
		t.Fatalf("init: %v", err)
	}

	got, err := rdb.SMembers(ctx, PortsFreeKey).Result()
	if err != nil {
		t.Fatalf("smembers: %v", err)
	}
	want := map[string]bool{"30000": true, "30002": true}
	if len(got) != len(want) {
		t.Fatalf("free = %v, want %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("free에 없어야 할 포트: %q", p)
		}
	}
}

// 풀 크기만큼 꺼내면 서로 다른 포트가 나오고, 그 다음엔 ok=false다.
func TestAllocatePortExhausts(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	cleanup(t, rdb, PortsFreeKey)

	if err := r.InitPortPool(ctx, 30000, 30002, nil); err != nil {
		t.Fatalf("init: %v", err)
	}

	seen := map[int]bool{}
	for i := 0; i < 3; i++ {
		p, ok, err := r.AllocatePort(ctx)
		if err != nil || !ok {
			t.Fatalf("allocate %d: ok=%v err=%v", i, ok, err)
		}
		if p < 30000 || p > 30002 {
			t.Errorf("할당 포트가 범위 밖: %d", p)
		}
		if seen[p] {
			t.Errorf("같은 포트를 두 번 할당: %d", p)
		}
		seen[p] = true
	}

	if _, ok, err := r.AllocatePort(ctx); ok || err != nil {
		t.Errorf("빈 풀에서 ok=%v err=%v, want ok=false", ok, err)
	}
}

// 반납한 포트는 다시 할당된다. 두 번 반납해도 풀엔 하나만 생긴다.
func TestReleasePortReturnsToPool(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	cleanup(t, rdb, PortsFreeKey)

	if err := r.InitPortPool(ctx, 30000, 30000, nil); err != nil {
		t.Fatalf("init: %v", err)
	}
	p, ok, err := r.AllocatePort(ctx)
	if err != nil || !ok || p != 30000 {
		t.Fatalf("allocate: p=%d ok=%v err=%v", p, ok, err)
	}

	// 같은 포트를 두 번 반납해도 중복이 생기면 안 된다.
	if err := r.ReleasePort(ctx, 30000); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := r.ReleasePort(ctx, 30000); err != nil {
		t.Fatalf("release 2: %v", err)
	}
	if n := rdb.SCard(ctx, PortsFreeKey).Val(); n != 1 {
		t.Fatalf("반납 후 풀 크기 = %d, want 1", n)
	}

	p2, ok, err := r.AllocatePort(ctx)
	if err != nil || !ok || p2 != 30000 {
		t.Fatalf("반납한 포트가 다시 안 나옴: p=%d ok=%v err=%v", p2, ok, err)
	}
}

// TCP 인스턴스는 port를 저장하고, Entry로 되읽는다. port 0이면 필드 자체가 없다.
func TestSaveStoresPort(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	withPort, noPort := unique("u-tcp"), unique("u-http")
	cleanup(t, rdb, key(withPort), key(noPort))

	if err := r.Save(ctx, withPort, "c-tcp", 30042, time.Time{}); err != nil {
		t.Fatalf("save with port: %v", err)
	}
	e, err := r.Entry(ctx, withPort)
	if err != nil || e == nil || e.Port != 30042 {
		t.Fatalf("Entry.Port = %v (err=%v), want 30042", e, err)
	}

	if err := r.Save(ctx, noPort, "c-http", 0, time.Time{}); err != nil {
		t.Fatalf("save no port: %v", err)
	}
	if ok := rdb.HExists(ctx, key(noPort), fieldPort).Val(); ok {
		t.Error("port 0인데 hash에 port 필드가 저장됨")
	}
	e2, err := r.Entry(ctx, noPort)
	if err != nil || e2 == nil || e2.Port != 0 {
		t.Fatalf("Entry.Port = %v (err=%v), want 0", e2, err)
	}
}

// reaper가 만료를 집행하려면 expiresAt이 저장되고 되읽혀야 한다(ADR-0014).
func TestSaveStoresExpiresAt(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	withExp, noExp := unique("u-exp"), unique("u-noexp")
	cleanup(t, rdb, key(withExp), key(noExp))

	exp := time.Now().Add(time.Hour).Truncate(time.Second).UTC()
	if err := r.Save(ctx, withExp, "c-1", 0, exp); err != nil {
		t.Fatalf("save: %v", err)
	}
	e, err := r.Entry(ctx, withExp)
	if err != nil || e == nil || !e.ExpiresAt.Equal(exp) {
		t.Fatalf("Entry.ExpiresAt = %v (err=%v), want %v", e, err, exp)
	}

	// zero면 필드를 안 쓴다.
	if err := r.Save(ctx, noExp, "c-2", 0, time.Time{}); err != nil {
		t.Fatalf("save no exp: %v", err)
	}
	if ok := rdb.HExists(ctx, key(noExp), fieldExpiresAt).Val(); ok {
		t.Error("expiresAt이 zero인데 hash에 필드가 저장됨")
	}
}

// reaper ①: 만료된 lease는 집합에서 빠지고 그 uuid: 기록도 지워진다. 안 만료된 건 남는다.
func TestPruneExpiredLeases(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	code := unique("web")
	old, fresh := unique("u-old"), unique("u-fresh")
	cleanup(t, rdb, sharedKey(code), leasesKey(code), key(old), key(fresh))

	now := time.Now()
	r.Join(ctx, code, old, now.Add(-time.Minute))  // 이미 만료
	r.Join(ctx, code, fresh, now.Add(time.Hour))    // 유효

	pruned, err := r.PruneExpiredLeases(ctx, code, now)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != old {
		t.Fatalf("pruned = %v, want [%s]", pruned, old)
	}
	// 만료된 참가 기록은 사라지고, 유효한 건 남는다.
	if e, _ := r.Entry(ctx, old); e != nil {
		t.Errorf("만료 uuid 기록이 남음: %+v", e)
	}
	if e, _ := r.Entry(ctx, fresh); e == nil {
		t.Error("유효 uuid 기록이 사라짐")
	}
	if n, _ := r.LeaseCount(ctx, code); n != 1 {
		t.Errorf("lease 개수 = %d, want 1", n)
	}
}

// reaper ②: idleSince는 처음 한 번만 찍힌다. 두 번째 호출은 기존 값을 안 덮는다.
func TestMarkIdleOnce(t *testing.T) {
	r, rdb := newTestRegistry(t)
	ctx := context.Background()
	code := unique("web")
	cleanup(t, rdb, sharedKey(code))
	r.SaveShared(ctx, code, "c-1") // idleSince 없음으로 시작

	first := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := r.MarkIdle(ctx, code, first); err != nil {
		t.Fatalf("mark1: %v", err)
	}
	// 두 번째는 다른 시각으로 — 덮어쓰면 안 된다.
	if err := r.MarkIdle(ctx, code, time.Now()); err != nil {
		t.Fatalf("mark2: %v", err)
	}
	s, err := r.Shared(ctx, code)
	if err != nil || s == nil {
		t.Fatalf("shared: %v", err)
	}
	if !s.IdleSince.Equal(first.UTC()) {
		t.Errorf("IdleSince = %v, want %v (첫 값 유지)", s.IdleSince, first.UTC())
	}
}
