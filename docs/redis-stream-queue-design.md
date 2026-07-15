# 채점 큐: Redis Streams 자료구조 및 장애 대응 설계

이 문서는 `worker/`(Go consumer)와 nimda-cite `backend-spring`(producer)이 공유하는
Redis Streams 큐의 **자료구조**와 **오류/장애 처리 아키텍처**를 정리한다. 결정 자체의
배경은 [ADR-0004](<./adr/[ADR-0004] Redis Streams 기반 채점 큐 선정.md>)를 참고하고,
이 문서는 "왜 이렇게 구현했는가"에 집중한다.
[Redis 공식 Streams 문서](https://redis.io/docs/latest/develop/data-types/streams/)를
기준으로 한다.

## 1. 왜 Redis Streams인가 (vs. List, Pub/Sub)

Redis로 큐를 만드는 방법은 여러 가지지만, 공식 문서는 용도별로 명확히 구분한다.

* **Pub/Sub**: 구독자가 없을 때 메시지가 그냥 사라짐(fire-and-forget). 채점 요청은 유실되면
  안 되므로 제외.
* **List (`RPUSH`/`BLPOP`)**: 메시지가 사라지진 않지만, 한 번 `BLPOP`으로 꺼내는 순간
  큐에서 완전히 제거된다. 워커가 꺼낸 직후 크래시하면 그 메시지는 **영구 유실**된다 (ack
  개념이 없음).
* **Streams (`XADD`/`XREADGROUP`)**: 메시지가 컨슈머에게 전달돼도 스트림에서 즉시 삭제되지
  않고, 각 Consumer Group의 **Pending Entries List(PEL)**에 "전달됐지만 아직 처리 확인
  안 됨" 상태로 남는다. 명시적으로 `XACK`을 호출해야 PEL에서 제거된다. 이 성질 덕분에
  워커가 죽어도 메시지를 복구할 수 있다.
  ([공식 문서: Consumer groups](https://redis.io/docs/latest/develop/data-types/streams/#consumer-groups))

채점 요청은 "받았다고 응답은 했는데 실제로 채점은 안 끝난 채 워커가 죽는" 상황이 실제로
발생할 수 있는 워크로드(컴파일/샌드박스 실행 중 OOM-kill, 프로세스 크래시 등)이므로 Streams를
선택했다.

## 2. 자료구조

### 2.1 메인 스트림 (`judge:submissions`)

`XADD`로 추가되는 각 엔트리는 Redis Stream 엔트리 하나이며, 필드는 모두 문자열이다
(Redis Streams는 필드-값 쌍의 map을 엔트리로 저장하는 append-only log 구조).

| 필드 | 타입(문자열로 저장) | 설명 |
|---|---|---|
| `submissionId` | Long | 채점 결과 콜백 시 그대로 돌려줘야 하는 식별자 |
| `problemId` | Long | 문제 ID |
| `userId` | Long | 제출자 ID |
| `language` | String | `JAVA` / `PYTHON3` / `CPP` / `C` |
| `sourceCode` | String | 제출 소스 코드 원문 |
| `timeLimitSeconds` | Double | 시간 제한(초) |
| `memoryLimitMb` | Integer | 메모리 제한(MB) |
| `submittedAt` | String (ISO-8601) | 제출 시각 |

정의 위치: producer(Java) `judgeServer.domain.submission.mq.SubmissionMessage`,
consumer(Go) `worker/internal/queue/message.go`의 `SubmissionMessage` + `ParseSubmissionMessage`.
두 쪽 다 **같은 필드 이름**을 계약으로 사용하며, 어느 한쪽만 바꾸면 파싱이 깨진다
(계약 문서: nimda-cite 저장소의 `docs/judge-queue-contract.md`).

### 2.2 Consumer Group (`judge-workers`)

Consumer Group은 별도 자료구조가 아니라, 스트림에 붙는 메타데이터다:
* **last-delivered-id**: 그룹이 어디까지 읽었는지 (서버 사이드에 저장되므로, 컨슈머 프로세스가
  재시작해도 읽은 위치를 잃지 않는다).
* **PEL (Pending Entries List)**: `(엔트리 ID, 소유 컨슈머, 마지막 전달 후 경과 시간, 전달 횟수)`
  튜플의 집합. `XREADGROUP`으로 읽힌 엔트리는 `XACK`되기 전까지 여기 남는다.
  ([공식 문서: XPENDING](https://redis.io/docs/latest/commands/xpending/))

프로듀서(`backend-spring`의 `JudgeQueueConfig`)와 컨슈머(`worker`의 `Consumer.EnsureGroup`)
양쪽 다 기동 시 `XGROUP CREATE ... MKSTREAM`을 시도하고 `BUSYGROUP` 에러는 무시하도록
구현했다. 두 서비스 중 어느 쪽이 먼저 떠도(배포 순서 무관) 그룹이 존재하게 만들기 위함이다.

### 2.3 Dead-letter 스트림 (`judge:submissions:dead-letter`)

Redis Streams는 RabbitMQ와 달리 DLQ가 내장돼 있지 않다. 재시도 한계를 넘긴 메시지를
버리지 않고 원본 필드 + 실패 사유(`failureReason`, `deliveryCount`, `originalId`)를 얹어
별도 스트림에 `XADD`한다 (`worker/internal/queue/consumer.go`의 `deadLetter`). 운영자가
나중에 `XRANGE`로 조회하거나, 원인 수정 후 원본 필드만 다시 `judge:submissions`에
재발행(replay)할 수 있다.

## 3. 오류 제어 (Error Handling)

컨슈머가 마주치는 실패는 성격이 서로 달라서, 하나의 "재시도" 정책으로 뭉뚱그리면 안 된다.
`worker/internal/queue/consumer.go`는 세 가지로 명확히 구분한다.

### 3.1 메시지 자체가 잘못된 경우 (poison message)

필수 필드 누락, 타입 불일치 등 — **재시도해도 절대 성공할 수 없는** 실패.
`ParseSubmissionMessage`가 에러를 반환하면 즉시:
1. 원본 + 실패 사유를 dead-letter 스트림에 기록
2. `XACK`으로 PEL에서 제거 (재시도 큐에 남겨봤자 영원히 실패하므로)

재시도하지 않고 즉시 격리하는 것이 핵심이다. 그렇지 않으면 이 메시지 하나가 매 claim 주기마다
계속 재시도되며 리소스를 낭비한다.

### 3.2 일시적 장애 (transient failure) — Redis 연결 자체의 문제

`XREADGROUP`/`XAUTOCLAIM` 호출 자체가 실패하는 경우(Redis 재시작, 네트워크 파티션 등).
Redis 공식 클라이언트 가이드에서 권장하는 대로 **지수 백오프(exponential backoff)**로 재연결을
시도한다 (`worker/internal/queue/backoff.go`). 이때 중요한 성질:

* Consumer Group의 last-delivered-id는 **Redis 서버 쪽에 저장**되므로, 클라이언트가 재연결에
  성공하면 이전에 읽지 못한 새 메시지부터 정상적으로 이어받는다. 클라이언트가 위치를 따로
  저장할 필요가 없다.
* 재연결 대기 중에도 이미 전달됐던(아직 ack 안 된) 메시지는 PEL에 그대로 남아있으므로 유실
  걱정이 없다.
* `NOGROUP` 에러(스트림/그룹이 삭제된 경우)는 별도로 감지해 그룹을 재생성한 뒤 계속한다.

기동 시점에는 별도로 `waitForRedis`(지수 백오프, `worker/cmd/worker/main.go`)로 Redis가
아직 안 떠 있어도(docker-compose 기동 순서 이슈 등) 크래시 루프에 빠지지 않게 했다.

### 3.3 비즈니스 처리 실패 (handler error) — 재시도 가능

채점 로직 자체에서 나는 에러(예: S3에서 테스트케이스 다운로드 타임아웃 등, ADR-0002 참고).
이 경우 **의도적으로 `XACK`을 호출하지 않는다.** 메시지는 PEL에 그대로 남고, 아래 3.4의
재할당(reclaim) 루프가 나중에 다시 넘겨준다.

### 3.4 워커 크래시/행(hang) 복구 — `XCLAIM`/`XAUTOCLAIM`

Redis Streams에는 서버 사이드 타임아웃이 없다 (즉, "이 컨슈머가 N초 동안 응답 없으면
자동으로 뺏어온다" 같은 기능이 없음). 그래서 공식 문서는 별도 루프로 PEL을 스캔해서
`XCLAIM`(또는 더 간단한 `XAUTOCLAIM`)으로 오래 방치된 엔트리를 스스로 회수하라고 안내한다
([공식 문서: Automatic claiming](https://redis.io/docs/latest/commands/xautoclaim/)).

구현(`Consumer.reclaimLoop`):
1. `JUDGE_QUEUE_CLAIM_INTERVAL`(기본 30초)마다 `XAUTOCLAIM`을 호출해, 마지막 전달 후
   `JUDGE_QUEUE_MIN_IDLE_TIME`(기본 60초) 넘게 idle 상태인 엔트리를 이 컨슈머 소유로 가져온다.
2. 회수한 각 엔트리에 대해 `XPENDING`(`XPendingExt`)으로 **누적 전달 횟수(delivery count)**를
   조회한다. 이 카운터는 Redis가 서버 사이드에서 관리하므로, 워커가 재시작해도 "몇 번
   재시도했는지" 기록이 살아있다 (로컬 메모리 카운터를 쓰면 재시작마다 리셋되는 문제를 피함).
3. `JUDGE_QUEUE_MAX_DELIVERIES`(기본 5회)를 넘겼으면 dead-letter로 이관 후 `XACK`.
   넘지 않았으면 정상 처리 경로(`process`)로 다시 넣어 재시도한다.

이 메커니즘 하나로 아래 장애 시나리오를 모두 복구한다:
* 워커 프로세스가 채점 도중 크래시/OOM-kill됨 → 다른 워커(또는 재시작된 자신)가
  `XAUTOCLAIM`으로 회수해 이어받음.
* 워커가 네트워크 파티션으로 Redis와 끊긴 채 계속 살아있다가 뒤늦게 같은 메시지를 처리하려는
  경우("split-brain") → 두 워커가 같은 메시지를 중복 처리할 수 있으므로, **핸들러는 반드시
  멱등(idempotent)해야 한다**(아래 3.5).
* 특정 제출물이 계속 처리에 실패하는 "독성 메시지"(예: 채점 로직 자체의 버그로 특정 언어
  조합에서 항상 예외) → 무한 재시도 대신 `MaxDeliveries` 이후 dead-letter로 격리되어 큐
  전체를 막지 않음.

### 3.5 멱등성(Idempotency) 요구사항

Consumer Group은 **at-least-once** 전달을 보장하지, exactly-once가 아니다
([공식 문서](https://redis.io/docs/latest/develop/data-types/streams/#consumer-groups)에도
명시). 즉, "채점은 끝냈는데 `XACK`이 Redis에 도달하기 전에 워커가 죽는" 경우 같은 제출물이
다시 배달될 수 있다. 따라서 `judge.Handler` 구현체(실제 채점 로직)는 반드시:
* 같은 `submissionId`를 두 번 처리해도 안전해야 한다 (예: 결과 콜백 API가 같은
  `submissionId`로 두 번 와도 최종 상태만 덮어쓰면 되도록 nimda-cite 쪽
  `SubmissionService#updateJudgeResult`는 이미 upsert 형태로 동작함).
* 채점 도중 부작용(과금, 알림 발송 등)이 있다면 별도로 중복 방지 로직이 필요함 — 현재
  스텁 핸들러(`worker/internal/judge/handler.go`)에는 부작용이 없으므로 이 부분은 실제
  채점 로직 구현 시 반영 필요.

## 4. 아직 다루지 않은 것 (한계와 후속 작업)

* **스트림 트리밍:** `XACK`은 PEL에서만 제거할 뿐 스트림 자체 엔트리는 지우지 않는다.
  운영 전 `XADD ... MAXLEN ~ N` 또는 주기적 `XTRIM`을 producer/운영 스크립트에 추가해야 한다.
* **Redis 자체의 이중화:** 현재 단일 Redis 인스턴스. Redis가 죽으면 큐 전체가 정지한다.
  Sentinel/Cluster 도입은 ADR-0004의 후속 과제로 남겨둠.
* **동시성/처리량 튜닝:** 현재 `Consumer.Run`은 메시지를 순차 처리한다. 채점은 CPU/샌드박스
  자원을 많이 쓰므로, 워커 내부 동시성보다는 **워커 프로세스 수를 늘리는 수평 확장**
  (같은 그룹에 컨슈머 이름만 다르게)이 기본 전략이다.
* **실제 채점 로직:** 이 저장소의 컨슈머는 큐 소비 뼈대만 구현했다. 컴파일/샌드박스 실행/
  S3에서 테스트케이스 다운로드/결과 콜백 POST는 `worker/internal/judge/handler.go`의
  `StubHandler`를 대체하는 실제 구현이 필요하다 (ADR-0001, ADR-0002 참고).