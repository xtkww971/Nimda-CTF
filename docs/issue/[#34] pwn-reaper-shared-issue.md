# [#34] SHARED idle 회수 — lease 만료 정리와 grace 후 컨테이너 내림

> 선행: #33 PER_USER reaper 완료. ADR-0019(SHARED lease·lock), ADR-0014(reaper) 참고합니다.
> 해당 이슈는 **공유(SHARED) 컨테이너의 수명 회수**를 다룰 예정입니다.
> #33에서 reaper는 `Isolation==PER_USER`만 봤습니다. 여기서 SHARED 처리를 더합니다.

### 배경

SHARED는 문제 하나에 컨테이너 하나를 모든 참가자가 같이 쓴다(ADR-0019).
참가자는 lease로 붙고, DESTROY는 자기 lease만 반납한다. 컨테이너는 남는다.
**컨테이너를 실제로 내리는 건 reaper만 한다고 ADR-0019가 정했는데, 그 reaper가 아직 SHARED를 삭제하지 않음.**

메모리 누수가 생기는 경우
* **아무도 사용하지 않는 SHARED 컨테이너가 영원히 존재.** 문제가 늘수록 안 쓰는 컨테이너가 리소스를 사용.
* **반납 안 한 lease가 쌓인다.** 참가자가 종료를 안 누르고 나가면 그 lease는 `T`가 지나도 남아, 컨테이너를 사용하는 것으로 판단한다.

ADR-0019 회수 규칙(⑤)를 추가해야 한다.

### 설계
```
각 shared:{code}에 대해
  ① 만료된 lease 정리 (참가자의 접근 종료)
     ZRANGEBYSCORE leases -inf now → 만료된 uuid 목록
       → uuid:{uuid} 삭제, ZREM
  ② lock 획득
     ZCARD leases == 0 이면
       idleSince 없음        → idleSince = now 저장
       now - idleSince ≥ G   → 컨테이너 삭제, shared:{code} 삭제
     lock 해제
```

**lock이 왜 필요한가 (PER_USER와 다른 점)**

PER_USER에서는 uuid가 컨테이너의 주인이라 reaper와 CREATE가 겹치지 않는다. 그래서 #33은 lock이 없다.

SHARED는 다르다. reaper가 "lease 0개"를 확인하고 컨테이너를 지우려는 사이에 새 참가자가 CREATE로 들어오면,
그 참가자는 곧 지워질 컨테이너 주소를 받는다(ADR-0019의 race).
그래서 ②는 CREATE가 쓰는 것과 **같은 lock**(`lock:shared:{code}`)을 잡고 해야 한다.
①(만료 lease 정리)은 lock 없이 해도 된다. 지우는 게 "이미 만료된 참가 기록"뿐이라 CREATE와 겹치지 않는다.

**grace period G를 두는 이유**

마지막 lease가 끝나자마자 내리면, 혼자 풀던 사람이 몇 초 뒤 다시 들어올 때 빌드를 기다린다(ADR-0019).
`G`(기본 10분, `CTF_SHARED_IDLE_GRACE`) 동안은 lease가 0개여도 남겨둔다.

**registry에 없는 것 — 새로 넣는다**

지금 registry는 lease를 더하고(`Join`) 빼고(`Leave`) 세는(`LeaseCount`) 것, idleSince를 지우는 것(`SaveShared`/`Join`)은 있다.
reaper에 필요한 건 없어서 추가한다.

* `PruneExpiredLeases(ctx, code, now) ([]string, error)`
  `ZREMRANGEBYSCORE leases -inf now`로 만료 lease를 빼고, 그 uuid들의 `uuid:` 기록을 지운다. 지운 uuid를 돌려준다(로그용).
* `MarkIdle(ctx, code, now) error`
  `shared:{code}`의 `idleSince`가 없을 때만 now로 채운다(HSETNX).

`Lock`/`Unlock`/`LeaseCount`/`Shared`/`DeleteShared`/`AllShared`는 이미 있다.

**reaper 모듈을 어떻게 확장하나**

#33의 `reap()`에 SHARED 패스를 더한다. `Store` 인터페이스에 위 메서드와 `AllShared`·`LeaseCount`·`Lock`·`Unlock`·`DeleteShared`를 추가한다.
PER_USER 패스와 orphan 패스는 그대로 두고, SHARED 패스를 나란히 돌린다.

```
reapShared(code, s):
  PruneExpiredLeases(code, now)          # ① lock 없이
  token, ok = Lock(code, ttl); if !ok: return   # ② 못 잡으면 다음 주기에
  defer Unlock
  if LeaseCount(code) == 0:
     if s.IdleSince.IsZero():  MarkIdle(code, now)
     elif now - s.IdleSince >= G:  Remove(s.InstanceID); DeleteShared(code)
```

### 작업

* [ ] `registry.go`: `PruneExpiredLeases`, `MarkIdle` 추가
* [ ] `reaper`: `Store`에 SHARED 메서드 추가, `reap()`에 SHARED 패스
* [ ] `reaper`: lock을 잡고 "lease 0 + grace 경과"일 때만 컨테이너·shared 기록 삭제
* [ ] `cmd/agent/main.go`: `CTF_SHARED_IDLE_GRACE`(기본 10m) 주입
* [ ] 테스트: 만료 lease 정리, lease 남으면 유지, 0개면 idleSince 찍기, grace 경과 후 삭제, lock 못 잡으면 건드리지 않음

### 완료 조건

* [ ] 만료된 lease가 다음 주기에 집합에서 빠지고 그 `uuid:` 기록도 지워진다
* [ ] lease가 하나라도 유효하면 컨테이너를 내리지 않는다
* [ ] lease가 0개가 된 순간 바로 안 내리고 `idleSince`를 찍는다
* [ ] `idleSince`로부터 `G`가 지나면 컨테이너와 `shared:{code}`를 지운다
* [ ] reaper가 지우려는 찰나 CREATE가 lock을 잡고 있으면, reaper는 이번 주기에 건드리지 않는다
* [ ] PER_USER 회수(#33)는 그대로 동작한다 (회귀 없음)
* [ ] `go build ./...`와 `go test ./agent/...`가 통과한다

### 이번에 하지 않는 것

* **SHARED 최대 수명·관리자 강제 재시작.** 계속 쓰이는 인기 문제는 안 내려간다(ADR-0019 단점). 강제 재시작은 모두를 끊는 동작이라 따로 설계한다.
* **TTL을 요청 메시지로 받기.** `sharedLeaseTTL=1h` 하드코딩 그대로. 회의 후 결정.
* **grace를 문제별로 다르게.** 지금은 호스트 전역 `G` 하나.
