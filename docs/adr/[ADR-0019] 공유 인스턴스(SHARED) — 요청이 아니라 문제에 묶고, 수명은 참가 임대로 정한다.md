* **날짜:** 2026-09-29
* **작성자:** 이도현
* **상태:** 제안됨 (protocol만 구현, 나머지는 아직 구현 안 함)

## 1. Context (배경)

PER_USER 요청 당 컨테이너 생성은 구현이 되어 있다.
요청이 오면 컨테이너를 하나 띄우고, `uuid:{uuid}`을 이용해
즉시 접근과 삭제를 처리한다.
이제 공유 컨테이너 SHARED를 추가해야 한다.

### SHARED가 무엇이고 어디에 쓰는가

SHARED는 문제 하나에 컨테이너를 하나만 띄우고 모든 참가자가 같이 쓰는 방식이다.
참가자가 많아져도 컨테이너는 하나라서 리소스를 적게 쓴다.

다만 아무 문제나 SHARED로 낼 수는 없다.
같은 컨테이너를 쓰기 때문에 한 사람이 바꾼 상태가 다른 사람에게도 그대로 보인다.

* **SHARED로 내도 되는 문제:** 참가자가 무엇을 해도 서버 상태가 바뀌지 않는 문제다.
  예를 들어 read-only DB에서 SQL injection으로 플래그를 꺼내는 문제가 여기에 해당한다.
* **SHARED로 내면 안 되는 문제:** pwn이나 stateful한 web 문제다.
  pwn은 셸을 따는 게 목표라서 한 명이 셸을 얻으면 모두의 문제가 망가진다.

그래서 isolation type은 단순 설정값이 아니라 보안과 직결되는 값이다.

### 이미 정해진 것

* **인증, 인가는 웹이 한다(ADR-0016, ADR-0018).** SHARED도 이 구조 안에서 동작해야 한다.
* **요청에 `isolationType`이 이미 들어 있다.** 웹은 SHARED 문제면 `SHARED`를 보낸다.
  그래서 웹에서 새로 만들 건 없다.
* **컨테이너는 agent만 관리한다(ADR-0009).** SHARED 판단과 정리도 agent가 한다.
* **수명은 문제 TTL `T`와 30초 주기 reaper로 관리한다(ADR-0014).** 다만 ADR-0014에서는 SHARED를
  "만료하지 않는다"로 남겨뒀다.

### PER_USER 구조를 그대로 쓸 수 있을까?

PER_USER는 "요청 하나에 컨테이너 하나"를 전제로 만들었다.
반면 SHARED는 "요청 여러 개에 컨테이너 하나"다.
이 차이 때문에 기존 구조를 그대로 쓰면 네 군데가 맞지 않는다.

**재사용 판단**

PER_USER는 `uuid:{uuid}`가 있으면 이미 만든 컨테이너로 본다.
하지만 웹은 요청마다 새 uuid를 발급한다.
SHARED는 uuid가 달라도 같은 문제면 이미 떠 있는 컨테이너를 줘야 한다.

**생성**

`runtime.Run`은 같은 이름의 컨테이너가 있으면 지우고 새로 띄운다.
PER_USER는 같은 사람이 다시 요청한 거라 상관없다.
하지만 SHARED에서는 다른 사람이 쓰던 컨테이너가 지워진다.

**삭제**

PER_USER는 uuid 주인이 곧 컨테이너 주인이라 DESTROY가 오면 컨테이너를 지운다.
SHARED는 한 명이 나가도 다른 사람이 쓰고 있을 수 있으니 컨테이너를 남겨야 한다.

**수명**

PER_USER는 요청 시점부터 `T`가 지나면 끝난다.
SHARED에 같은 규칙을 쓰면 처음 띄운 사람 기준으로 모두가 한꺼번에 끊긴다.

실제로 SHARED 요청을 PER_USER 로직으로 처리해봤다(`web-basic-01`, 사용자 A·B).
B가 요청하자 이미지를 다시 빌드하고 A의 컨테이너를 지운 뒤 새로 띄웠다.
A가 다시 요청하니 이번엔 B의 컨테이너가 지워졌다.
위의 재사용 판단, 생성 문제가 그대로 나온 것이다.

**같이 정해야 하는 것**

SHARED를 추가하면서 두 가지를 더 정해야 한다.

* **플래그:** DYNAMIC은 참가자마다 다른 플래그를 env로 넣는다.
  그런데 컨테이너가 하나면 플래그도 하나만 넣을 수 있다.
* **isolation type 검증:** 지금까진 PER_USER 하나뿐이라
  `!= "PER_USER"`면 나머지로 처리해도 문제가 없었다.
  하지만 값이 두 개가 되면 오타 같은 잘못된 값이 어디로 가는지가 중요해진다.
  잘못된 값이 SHARED로 가면 pwn 문제가 모두에게 열릴 수 있다.

## 2. Decision (결정)

* **SHARED 인스턴스는 요청(uuid)이 아니라 문제(challengeCode)에 매핑된다.**
  uuid는 컨테이너가 아니라 "이 사람이 이 문제에 들어왔다"는 기록이 된다.
* **수명은 lease로 관리한다.**
  참가자마다 lease를 하나씩 주고, 유효한 lease가 하나도 없을 때만 컨테이너를 내린다.

### isolation type은 문자열 그대로 두고, 잘못된 값은 reject한다

`protocol`에 타입과 상수를 추가한다.

```go
type IsolationType string

const (
    IsolationShared  IsolationType = "SHARED"
    IsolationPerUser IsolationType = "PER_USER"
)
```

* **메시지 값은 바꾸지 않는다.** 웹, provisioner, agent는 이미 `"SHARED"`, `"PER_USER"`
  문자열로 통신하고 있다. 타입을 추가하는 건 Go 코드 안의 변경이라 메시지 형식은 그대로다.
* **두 값 외에는 reject한다.** `ParseRequestMessage`에서 잘못된 값을 막는다.
  agent 분기도 `switch`로 두 케이스를 명시하고 `default`는 에러로 처리한다.
  isolation type이 잘못됐을 때는 덜 격리된 쪽으로 가는 것보다 아예 안 뜨는 게 안전하다.
* **SHARED + DYNAMIC 조합은 reject한다.** parser에서 막고, 웹도 문제 등록 단계에서 막는다.
  웹의 `CtfRequestMessage.of`에도 같은 검사가 들어가 있다.
  여러 군데서 막는 건 한 곳이 빠져도 플래그가 잘못된 컨테이너가 뜨지 않게 하기 위해서다.

**bool(`shared: true`)로 하면 더 간단하지 않을까?**

bool은 값이 없으면 false가 된다.
그러면 필드가 빠진 메시지가 와도 에러 없이 한쪽으로 처리된다.
잘못된 값을 막으려고 타입을 만드는 건데 bool로는 그걸 못 한다.

### 새 action은 만들지 않는다

JOIN 같은 action을 추가하지 않고 CREATE를 그대로 쓴다.

웹 입장에서는 컨테이너를 새로 만들든 이미 떠 있는 데 붙든 똑같기 때문이다.
웹은 "이 문제에 들어가고 싶다"고 요청하고 접속 정보와 만료 시각만 받으면 된다.
새로 만들지 기존 걸 쓸지는 agent가 isolation type을 보고 판단한다.

그래서 이번에 바뀌는 메시지 규칙은 위의 검증뿐이다.
(TTL 필드는 ADR-0014에서 추가하기로 했다.)

### registry는 컨테이너 정보와 참가 정보를 나눈다

```
uuid:{uuid}              참가 정보 (요청마다 하나)
  PER_USER → { isolation: PER_USER, instanceId, expiresAt }
  SHARED   → { isolation: SHARED,  challengeCode, expiresAt }    ← 컨테이너 ID는 저장하지 않는다

shared:{challengeCode}          공유 컨테이너 정보 (문제마다 하나)
  → { instanceId, createdAt, idleSince }

shared:{challengeCode}:leases   유효한 lease 목록 (sorted set)
  → member = uuid, score = lease 만료 시각(unix)

lock:shared:{challengeCode}     생성과 정리가 겹치지 않게 하는 lock (SET NX PX)
```

**SHARED의 `uuid:{uuid}`에는 왜 컨테이너 ID 대신 challengeCode를 넣을까?**

공유 컨테이너가 죽어서 다시 만들어지면 컨테이너 ID가 바뀐다.
uuid마다 컨테이너 ID를 저장해두면 ID가 바뀔 때마다 전부 찾아서 수정해야 한다.
하나라도 놓치면 죽은 컨테이너를 가리키는 데이터가 남는다.

challengeCode로 `shared:{code}`를 한 번 더 조회하게 하면 `shared:{code}` 하나만 수정하면 된다.
모든 uuid가 자동으로 새 컨테이너를 보게 된다.

PER_USER는 지금과 같은 정보를 저장한다.
값을 string에서 hash로 바꾸는 건 ADR-0014에서 만료 시각을 저장하려면 어차피 해야 하는 작업이다.
여기에 `isolation`과 `challengeCode` 필드만 추가한다.

### CREATE 흐름

**PER_USER**는 지금과 같다. 만료 시각 저장만 ADR-0014를 따른다.

**SHARED**는 아래 순서로 처리한다.

```
lock:shared:{code} 획득 (실패하면 에러 반환 → ACK 안 함 → redelivery 때 재시도)
  │
  ├─ shared:{code} 있음 + 컨테이너 running
  │     → 빌드 생략, build context만 정리
  │
  └─ 없음 또는 죽어 있음
        → 빌드 + 실행, shared:{code} 저장 (instanceId 교체)
  │
  ZADD NX shared:{code}:leases  uuid  (now + T)
  uuid:{uuid} = { SHARED, code, expiresAt = lease 만료 }
  shared:{code}.idleSince 삭제
lock 해제
→ READY { host, port, expiresAt = 이 참가자의 lease 만료 }
```

**`ZADD NX`를 쓰는 이유**

Redis Stream은 at-least-once라서 같은 메시지가 두 번 이상 올 수 있다.
같은 요청이 다시 왔을 때 lease가 연장되면 안 되기 때문에, 이미 있으면 건드리지 않는 `NX`를 쓴다.
lease 연장은 나중에 renew로 따로 만든다.

**lock이 왜 필요할까?**

agent는 메시지를 순서대로 처리하기 때문에 CREATE끼리는 겹치지 않는다.
하지만 reaper(ADR-0014)는 별도 goroutine으로 돈다.

reaper가 "lease가 0개"인 걸 확인하고 컨테이너를 지우려는 사이에 새 참가자가 들어왔다고 가정하자.
그 참가자는 곧 지워질 컨테이너 주소를 받게 된다.
CREATE와 reaper가 같은 lock을 잡아야 이 race condition을 막을 수 있다.

lock TTL은 빌드 시간보다 길게 잡는다.
빌드 중에 lock이 풀리면 두 번째 빌드가 첫 번째 컨테이너를 지워버리기 때문이다.

### DESTROY 흐름

**PER_USER**는 지금과 같다. 컨테이너와 데이터를 지운다.

**SHARED**는 자기 lease만 반납한다.
`ZREM`으로 lease를 빼고 `uuid:{uuid}`를 지운 뒤 DESTROYED를 반환한다.
컨테이너는 건드리지 않는다.

참가자 입장에서 DESTROYED는 "내 접근이 끝났다"는 뜻이다.
PER_USER와 SHARED 둘 다 같은 의미로 쓴다.

공유 컨테이너를 실제로 내리는 건 reaper만 한다.
관리자 강제 재시작 기능은 이번에 넣지 않는다.

### TTL은 lease에 걸고, 컨테이너는 "lease 0개 + grace period"가 지나면 내린다

| 대상 | 수명 | 정하는 곳 |
| --- | --- | --- |
| SHARED lease | `T` (문제 TTL, 기본 1시간) | 웹 `Challenge` → 요청 메시지 |
| SHARED 컨테이너 | lease가 남아 있는 동안 + 마지막 lease가 끝난 뒤 grace period `G` | agent |
| grace period `G` | 10분 (`CTF_SHARED_IDLE_GRACE`) | agent 설정 |
| 응답 `expiresAt` | 이 참가자의 lease 만료 시각 | agent (ADR-0014) |

reaper(30초 주기)에는 SHARED 처리를 이렇게 추가한다.

```
각 shared:{code}에 대해
  ① ZRANGEBYSCORE leases -inf now → 만료된 uuid 목록
     → uuid:{uuid} 삭제, ZREM                          (참가자의 접근 종료)
  ② lock 획득
     ZCARD leases == 0 이면
       idleSince 없음        → idleSince = now 저장
       now - idleSince ≥ G   → 컨테이너 삭제, shared:{code} 삭제
     lock 해제
```

이 방식을 고르기 전에 다른 방법들도 검토했다.

**(A) 만료하지 않는다**

ADR-0014에서 SHARED에 대해 남겨둔 방식이다.
문제당 하나라서 컨테이너 수가 문제 수를 넘지 않는다는 게 이유였다.

하지만 상시 워게임은 모든 문제가 항상 열려 있다.
문제가 늘어날수록 아무도 안 푸는 문제의 컨테이너가 리소스를 계속 잡고 있게 된다.
응답에 `expiresAt`이 없어서 웹이 Redis 키 TTL을 계산할 수도 없다.
그래서 이 ADR로 ADR-0014의 "SHARED는 만료하지 않는다"를 대체한다.

**(B) 생성 시점부터 고정 TTL**

PER_USER와 같은 방식이다.
하지만 공유 컨테이너가 만료되는 순간 그때 풀고 있던 사람 전부가 한 번에 끊긴다.

**(C) 마지막 접근 기준 idle timeout**

제일 흔한 방식이지만, 우리 구조에서는 접근 여부를 agent가 알 수 없다.
참가자 트래픽은 웹 proxy를 지나가고, agent는 CREATE만 받는다.

이걸 쓰려면 웹이 요청마다 CTF에 "접근했다"를 보내야 한다.
그러면 매 요청마다 write가 생기고, CTF가 참가자 트래픽을 알게 된다.
사람 관련은 웹, 리소스 관련은 CTF가 맡는다는 역할 분리(ADR-0016, 0018)도 흐려진다.

**(D) 참가자 수 카운터**

들어오면 +1, 나가면 -1, 0이 되면 내린다.
단순하지만 -1을 한 번이라도 놓치면 카운터가 영원히 0이 안 된다.
agent가 재시작되거나 참가자가 종료를 안 누르고 나가면 쉽게 놓친다.
한 번 틀어진 값은 스스로 복구되지 않는다.

**(E) 만료 시각이 있는 lease 집합 (채택)**

카운터 대신 "누가 언제까지 쓰는지"를 저장한다.

* **반납 안 해도 정리된다.** 참가자가 그냥 나가도 lease는 `T`가 지나면 알아서 만료된다.
* **참가자 한 명 기준으로는 PER_USER와 같다.** 각자 `T`를 받고, 끝나면 그 사람만 끝난다.
* **`expiresAt`이 항상 있다.** 웹은 isolation type과 상관없이 같은 방식으로 Redis 키 TTL을 계산한다.
* **만료된 것만 바로 꺼낼 수 있다.** sorted set은 score(만료 시각) 순으로 정렬되어 있기 때문이다.

**grace period를 10분으로 잡은 이유**

마지막 lease가 끝나자마자 컨테이너를 내리면 바로 다음 사람이 빌드를 기다려야 한다.
혼자 푸는 문제에서는 lease가 끝나고 몇 초 뒤에 다시 들어오는 경우가 많다.
10분은 ADR-0014의 Redis 키 TTL 여유와 같은 값이라 일단 맞춰둔다.

### 웹은 바꾸지 않는다

웹은 지금처럼 `isolationType=SHARED`를 보내고 요청마다 새 uuid를 쓴다.
응답 형식과 `expiresAt` 의미도 PER_USER와 같다.

ownership 검사는 uuid 단위로 하기 때문에, 같은 컨테이너를 가리키더라도 남의 uuid로는 접근할 수 없다.
테스트에서도 남의 uuid는 403, 비로그인은 401이 나왔다.

## 3. Consequences (결과 및 영향)

### 🟢 장점

* **리소스를 적게 쓴다.** 참가자가 몇 명이든 문제당 컨테이너는 하나다.
* **빠르게 접속된다.** 컨테이너가 떠 있으면 빌드 없이 Redis write 몇 번으로 끝난다.
* **한 명이 모두를 끊을 수 없다.** SHARED에서 DESTROY는 자기 lease 반납일 뿐이다.
* **안 쓰는 컨테이너는 정리된다.** 컨테이너 수가 "문제 수"가 아니라 "지금 풀리고 있는 문제 수"가 된다.
* **isolation type이 잘못되면 안 뜬다.** 오타 때문에 pwn 문제가 SHARED로 뜨는 일이 없다.
* **웹과 메시지 형식은 거의 그대로다.** 새 action도 새 필드도 없고, 검증만 추가됐다.

### 🔴 단점 / 개선안

**혼자 오래 푸는 참가자는 `T`에서 끊긴다**

PER_USER와 같은 문제고, 나중에 renew로 해결한다.
그 전까지는 다시 요청하면 같은 컨테이너에 새 lease로 들어온다.
SHARED는 stateless한 문제라 다시 들어와도 잃는 게 없다.

**계속 쓰이는 컨테이너는 재시작되지 않는다**

참가자가 끊이지 않는 인기 문제는 한 번도 내려가지 않는다.
누군가 디스크를 채우거나 프로세스를 죽이면 모두가 망가진 컨테이너를 쓰게 된다.
최대 수명이나 관리자 강제 재시작이 필요할 수 있지만 지금은 넣지 않는다.
동아리 규모에서는 드문 일이고, 강제 재시작은 모두를 끊는 동작이라 따로 설계가 필요하다.

**주소가 바뀔 수 있다**

공유 컨테이너가 죽었다가 다시 뜨면 포트가 바뀐다.
웹이 10분짜리 result 캐시에서 주소를 읽는 동안에는 기존 참가자가 이전 포트로 접속하게 된다.
Proxy.md에 적은 "매 요청 CTF에 주소를 물어본다"가 같이 필요하다.

**컨테이너 이름으로 라우팅할 수 없다**

ADR-0017은 컨테이너 이름을 uuid로 두고 이름으로 라우팅하는 방식을 제안했다.
SHARED는 여러 uuid가 컨테이너 하나(`ctf-{code}`)를 가리키기 때문에 이 방식을 쓸 수 없다.
주소는 registry를 통해서 찾아야 한다.

**lock이 하나 늘었다**

빌드 중에 lock이 먼저 만료되면 빌드가 두 번 돈다.
lock TTL을 빌드 시간보다 길게 두고, 빌드 timeout도 따로 정해야 한다(지금은 없다).

**여러 모듈을 수정해야 한다**

모듈 하나에 작업 하나 규칙에 따라 순서대로 나눈다.

1. `protocol`: `IsolationType` 타입, 값 검증, SHARED + DYNAMIC reject (완료)
2. `agent/internal/registry`: hash 구조, `shared:` 데이터와 lease, lock
3. `agent/internal/agentqueue`: CREATE, DESTROY의 SHARED 분기
4. reaper: ADR-0014 작업에 SHARED 처리 추가

3번은 2번이 끝난 다음에 해야 한다.
지금의 string registry 위에 먼저 만들면 다시 고쳐야 하기 때문이다.

## 4. 참고자료

### 내부 문서

* [ADR-0010](<./[ADR-0010] 인스턴스 생성 파이프라인 구조.md>): "공유 인스턴스 재사용 미구현"으로
  남겨둔 작업. 이 문서가 그 설계다.
* [ADR-0014](<./[ADR-0014] 인스턴스 수명(TTL) 정책 — 선언 대신 집행.md>): 문제 TTL `T`, reaper,
  Redis 키 TTL 규칙. "SHARED는 만료하지 않는다"를 이 문서가 대체한다.
* [ADR-0016](<./[ADR-0016] 인스턴스 접근 인가 — 게이트웨이는 판정하지 않는다.md>),
  [ADR-0018](<./[ADR-0018] 인증 인가 위치 확정.md>): 사람 관련은 웹, 리소스 관련은 CTF.
  idle timeout(C)을 고르지 않은 이유.
* [ADR-0017](<./[ADR-0017] 문제 컨테이너 격리 경계 — 도메인·네트워크·내부 API.md>): 컨테이너 이름 =
  uuid 라우팅. SHARED에서는 쓸 수 없다.
* `docs/features/CreateInstance.md`: uuid와 인스턴스 매핑의 원래 설계.
* `SHARED-issue.md`: 이 기능의 작업 목록과 완료 조건.

### 코드

* `agent/internal/agentserver/handler.go`: `instanceName`의 `!= "PER_USER"` 분기,
  `PER_USER`일 때만 채우는 `ExpiresAt`.
* `agent/internal/runtime/docker.go`: `Run`이 같은 이름의 컨테이너를 먼저 지운다
  (`_ = d.Remove(ctx, name)`).
* `agent/internal/agentqueue/dispatcher.go`: `// todo SHARED인 경우와 PER_USER인 경우 작업 분리`.
  SHARED 분기가 들어갈 자리.
* `agent/internal/registry/registry.go`: `uuid:{uuid} → instanceID` string 저장.
* `protocol/stream.go`: isolation type 검증과 SHARED + DYNAMIC reject (구현됨).
* 웹 `CtfRequestMessage.of`: SHARED + DYNAMIC 조합 reject.
