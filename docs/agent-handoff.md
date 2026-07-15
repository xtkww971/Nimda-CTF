# Agent Handoff: 채점 큐 (Producer/Consumer) 작업 요약

이 문서는 다른 에이전트(또는 사람)가 이어받아 작업할 때 빠르게 맥락을 잡을 수 있도록,
지금까지 AI 에이전트가 두 개 저장소에 걸쳐 진행한 작업을 정리한 것이다. 코드 자체가
진실의 원천(source of truth)이고, 이 문서는 "왜 이렇게 됐는지"를 보충하는 지도 역할만 한다.

## 관련 저장소 2개

| 저장소 | 경로 | 역할 |
|---|---|---|
| nimda 웹 백엔드 |  (`NimdaConBackEnd/backend-spring`) | **Producer**. 사용자가 코드를 제출하면 채점 큐에 발행 |
| 채점 서버 (본 저장소) | `Judge Server` | **Consumer**. 큐에서 제출물을 꺼내 채점 (실제 채점 로직은 아직 스텁) |

두 저장소는 Redis(`judge:submissions` 스트림)를 통해서만 통신하며, 코드 레벨 의존성은 없다.
계약(메시지 필드, 스트림/그룹 이름)은 코드 주석이 아니라 아래 문서들로 명시적으로 관리한다:
* nimda-cite 쪽: `docs/judge-queue-contract.md`
* 본 저장소 쪽: `docs/redis-stream-queue-design.md`, `docs/adr/[ADR-0004] Redis Streams 기반 채점 큐 선정.md`

## 지금까지 한 일 (시간순)

### 1단계 — Producer (nimda-cite `backend-spring`)
기존에 이미 `judgeServer` 패키지(Submission/Problem 도메인)가 존재했고,
`SubmissionService.submit()`에 "채점 요청 전송 로직 추가해야함" TODO와
`POST /api/judge/submission/result` 콜백 API는 이미 구현돼 있었음. 이번 작업으로 그 TODO를
채웠다:
* `judgeServer.domain.submission.mq.SubmissionMessage` — 큐 메시지 DTO
* `judgeServer.domain.submission.mq.JudgeQueueProperties` — `judge.queue.*` 설정
* `judgeServer.domain.submission.mq.SubmissionProducer` / `RedisSubmissionProducer` — `XADD` 발행
* `judgeServer.config.JudgeQueueConfig` — 기동 시 `XGROUP CREATE ... MKSTREAM` (idempotent)
* `SubmissionService.submit()`에 발행 로직 연결
* `docs/judge-queue-contract.md` — 컨슈머(스택 미정 상태였음)를 위한 계약 문서

컴파일 확인만 했고(mvn compile), 실제 Redis에 붙여서 통합 테스트는 안 함.

### 2단계 — Consumer (본 저장소, Go)
사용자가 "web controller 삭제, Go로만 큐 소비하는 틀, Redis 공식문서 기반 근거, 오류 제어를
자세하게, 장애 대응을 최대한"이라고 요청. 진행한 일:

* `webController/`(빈 Spring Boot 껍데기, 실제 로직 없었음) 삭제
* `worker/` Go 모듈 신설 (module: `nimda-judge-worker`, 의존성은 `github.com/redis/go-redis/v9`
  하나뿐 — 로깅은 표준 라이브러리 `log/slog` 사용)
  * `cmd/worker/main.go` — 엔트리포인트. Redis 연결 재시도(backoff), 그레이스풀 셧다운
    (SIGINT/SIGTERM → context 취소)
  * `internal/config/config.go` — 환경변수 기반 설정 (기본값은 producer 쪽과 동일한
    stream/group 이름)
  * `internal/queue/message.go` — `SubmissionMessage` 파싱 (producer의 Java DTO와 1:1 대응)
  * `internal/queue/consumer.go` — 핵심 로직: `XREADGROUP` 읽기 루프 + `XAUTOCLAIM` 기반
    회수(reclaim) 루프 + dead-letter 이관 + 3가지 에러 클래스 구분 처리
  * `internal/queue/backoff.go` — 지수 백오프
  * `internal/judge/handler.go` — **스텁**. 실제 채점 로직(컴파일/샌드박스/S3/결과 콜백)은
    아직 없고, 로그만 찍고 성공 반환함
* `docs/adr/[ADR-0003] 채점 워커 언어로 Go 선정.md` — Go를 채점 워커 언어로 선정한 근거
  (이슈 #2 체크리스트 항목)
* `docs/adr/[ADR-0004] Redis Streams 기반 채점 큐 선정.md` — RabbitMQ(기존 README 뱃지에
  전제돼 있었으나 실제 도입된 적 없음) 대신 Redis Streams를 쓰기로 한 결정 기록 (이슈 #2
  체크리스트 항목)
* `docs/redis-stream-queue-design.md` — 자료구조 + 오류 제어/장애 대응 상세 설계, 전부
  [Redis 공식 문서](https://redis.io/docs/latest/develop/data-types/streams/) 근거로 작성

**로컬에서 실제 Redis 컨테이너(docker)로 3가지 시나리오를 직접 검증함:**
1. 정상 처리 → `XACK` 확인 (PEL 비어있음)
2. 필수 필드 누락 메시지 → 즉시 dead-letter 이관 + `XACK` (무한 재시도 안 함)
3. "크래시한 컨슈머"를 `redis-cli`로 흉내내서(읽고 ack 안 함) → `reclaimLoop`이
   `XAUTOCLAIM`으로 회수해서 재처리하고 정상 완료하는 것까지 확인 (delivery count가
   서버 사이드에 정확히 2로 집계됨)

`go build`, `go vet` 통과. 유닛 테스트는 작성 안 함(스텁 핸들러라 테스트할 로직이 아직 적음).

## 아직 안 된 것 (다음에 할 일)

* **실제 채점 로직**: `worker/internal/judge/handler.go`의 `StubHandler`를 대체.
  ADR-0002(S3에서 테스트케이스 다운로드) + 언어별 컴파일/샌드박스 실행 + 결과를
  `POST /api/judge/submission/result`로 콜백하는 로직 필요.
* **스트림 트리밍**: `XACK`된 엔트리도 스트림에는 그대로 남음. `MAXLEN`/`XTRIM` 정책 미구현
  (두 저장소 문서 모두에 후속 과제로 명시해 둠).
* **README 갱신**: 본 저장소 최상위 `README.md`가 아직 RabbitMQ 뱃지를 달고 있음
  (ADR-0004와 불일치). 이번 작업 범위에서는 의도적으로 건드리지 않음 — 사용자 확인 필요.
* **Dockerfile / CI**: `worker/`에 아직 없음 (요청 범위 밖이라 스킵).
* **Redis 이중화**: 단일 인스턴스라 Redis 장애 시 전체 큐 정지. 후속 과제로만 문서화.
* **테스트 코드**: `worker`는 수동으로 Redis 컨테이너에 대고 시나리오 검증만 했고, Go
  유닛/통합 테스트 파일은 없음.

## 다음 작업 시 참고 사항

* 두 저장소의 stream key/consumer group 이름은 **환경변수 이름이 다르다** — nimda-cite는
  Spring 프로퍼티(`judge.queue.stream-key`)를 `JUDGE_QUEUE_STREAM_KEY` env로 오버라이드하고,
  Go 워커는 직접 `JUDGE_QUEUE_STREAM_KEY`를 읽는다. 이름은 같게 맞춰뒀지만, 기본값을 한쪽만
  바꾸면 어긋난다 — 바꿀 때 두 저장소 모두 확인할 것.
* `worker`의 핸들러는 **멱등이어야 한다** (at-least-once 전달이라 같은 제출물이 두 번 올 수
  있음). 실제 채점 로직을 넣을 때 이 전제를 깨지 않도록 주의 (`docs/redis-stream-queue-design.md`
  3.5절 참고).
* **이슈 #4의 DTO 스펙과 실제 구현이 다르다.** 이슈 #4에 정의된 `JudgeMessageDto`는
  `submissionId`/`problemId`/`language`/`sourceCode` 4개 필드만 명시하는데, 실제 구현
  (`SubmissionMessage`, 양쪽 저장소 동일)에는 `userId`/`timeLimitSeconds`/`memoryLimitMb`/
  `submittedAt`이 추가돼 있다 (Problem 엔티티의 제한 시간/메모리를 채점 서버가 매번 DB로
  재조회하지 않도록 하기 위함). 이슈 #4는 "discussion" 라벨이 붙어 있어 스펙이 아직 유동적일
  수 있음 — 필드를 바꾸기 전에 이슈 코멘트로 먼저 합의할 것.