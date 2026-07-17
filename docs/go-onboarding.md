# 채점 서버(Go Worker) 구조 파악 & 온보딩 문서

이 문서는 Go를 전혀 모르는 상태에서 이 저장소(`Judge Server`)를 이어받아 작업하기 위해
현재 구현 상태, 폴더 구조, 두 저장소(Spring 백엔드 ↔ Go 채점 서버) 간 계약, 그리고
앞으로 무엇을 공부해야 하는지를 정리한 것이다. 코드가 아니라 문서만 작성한 결과물이다.

---

## 1. 전체 그림: MSA 구조와 지금까지의 결정

```
[사용자] → SubmissionController → SubmissionService.submit()
                                        │
                                        ▼
                          RedisSubmissionProducer (XADD)
                                        │
                              Redis Stream: judge:submissions
                                        │
                                        ▼
                        Go worker (XREADGROUP, consumer group: judge-workers)
                                        │
                              judge.Handler (지금은 스텁)
                                        │
                    (앞으로) 컴파일 → 샌드박스 실행 → 채점 → 결과 콜백
                                        │
                                        ▼
                POST /api/judge/submission/result (Spring 백엔드, 아직 미연동)
```

* **Producer**: `NimdaConBackEnd/backend-spring` (Spring Boot). 사용자가 제출하면
  `SubmissionService.submit()`이 `SubmissionMessage`를 만들어 Redis Stream에 `XADD`.
* **Consumer**: 이 저장소 `Judge Server/worker` (Go). 스트림에서 꺼내 채점하는 역할.
* **연결 방식**: 코드 레벨 의존성 없음. **Redis Stream(`judge:submissions`)만 공유**.
  즉 두 저장소는 서로의 코드를 몰라도 되고, "메시지 필드 이름/타입"과 "스트림/그룹 이름"이라는
  **계약(contract)**만 맞으면 된다.

이 결정들의 배경은 ADR 4개에 문서화되어 있다 (`docs/adr/`):
* ADR-0001: API 레이어는 Spring Boot
* ADR-0002: 테스트케이스는 S3에 저장, 큐에는 URL만
* ADR-0003: 채점 워커(consumer)는 Go로 선정
* ADR-0004: 메시지 큐는 RabbitMQ가 아니라 **Redis Streams**

## 2. 지금 실제로 구현되어 있는 것 / 안 되어 있는 것

### 되어 있는 것
* Producer(Spring) → Redis Stream 발행 (`RedisSubmissionProducer`, `JudgeQueueConfig`)
* Consumer(Go) → 큐 소비 뼈대: 정상 읽기, ACK, 잘못된 메시지 즉시 격리(dead-letter),
  워커 크래시 시 자동 회수(XAUTOCLAIM), 재시도 횟수 초과 시 dead-letter 이관,
  Redis 자체가 끊겼을 때 지수 백오프 재연결
* 로컬 Redis 컨테이너로 위 3가지 장애 시나리오는 실제로 검증됨 (docker로 직접 테스트)

### 스텁(stub)이라 안 되어 있는 것 — **여기가 앞으로 할 일**
* `worker/internal/judge/handler.go`의 `StubHandler`는 로그만 찍고 끝냄.
  실제 채점 로직(컴파일 / 샌드박스 실행 / 결과 판정 / 결과 콜백 POST)이 전혀 없음.
* S3에서 테스트케이스 다운로드 로직 없음 (ADR-0002 전제).
* 결과를 `POST /api/judge/submission/result`로 돌려주는 클라이언트 코드 없음.
* 스트림 트리밍(`XTRIM`/`MAXLEN`) 미구현 — ACK된 메시지도 스트림에 영원히 쌓임.
* `worker/`에 Dockerfile 없음. (`debuging tool for window/JudgeServer/docker-compose.yml`은
  아직 옛날에 삭제된 `webController/`를 가리키고 있어 **현재 구조와 불일치** — 나중에 갱신 필요)
* Go 유닛/통합 테스트 파일 전무.
* Redis 이중화(단일 인스턴스라 장애 시 큐 전체 정지) — 후속 과제로만 문서화됨.

자세한 이력은 `docs/agent-handoff.md`에 시간순으로 정리되어 있으니, 다음 작업 시작 전 먼저 읽는 것을 권장.

## 3. Go 폴더 구조 (Java/Spring과 대응해서 설명)

```
Judge Server/
├── docs/                     # ADR, 설계 문서, 온보딩 문서 (이 파일 포함)
├── debuging tool for window/ # 로컬 docker-compose 실행용 (Windows 전용, 현재 일부 outdated)
├── README.md, CONTRIBUTING.md
└── worker/                   # Go 모듈 루트 (Spring 프로젝트의 backend-spring 폴더에 해당)
    ├── go.mod                # 모듈 이름 + Go 버전 + 의존성 선언 (pom.xml/build.gradle 대응)
    ├── go.sum                # 의존성 체크섬 잠금 파일 (package-lock.json 대응)
    ├── cmd/
    │   └── worker/
    │       └── main.go       # 엔트리포인트. package main + func main() (Spring의 @SpringBootApplication + main 대응)
    └── internal/             # 이 모듈 밖에서는 import 불가 (Go 언어 자체 강제 규칙)
        ├── config/
        │   └── config.go     # 환경변수 → Config 구조체 (application.yml 대신 env var 사용)
        ├── queue/
        │   ├── consumer.go   # 핵심 로직: XREADGROUP 읽기 루프 + XAUTOCLAIM 회수 루프 + 에러 분기
        │   ├── message.go    # SubmissionMessage 구조체 + 파싱 (Java의 DTO + 검증 로직 대응)
        │   └── backoff.go    # 지수 백오프 유틸
        └── judge/
            └── handler.go    # StubHandler (실제 채점 로직이 들어갈 자리, 여기가 메인 작업 대상)
```

### Go 특유의 관례 (Java 개발자 기준 대응표)

| Go 개념 | Java/Spring 쪽 유사 개념 | 비고 |
|---|---|---|
| `go.mod` | `pom.xml` / `build.gradle` | 모듈명, Go 버전, 의존성 |
| `package` (폴더 = 패키지) | `package` (파일 상단 선언) | Go는 **폴더 하나 = 패키지 하나**가 원칙 |
| `cmd/worker/main.go` | `@SpringBootApplication` 붙은 메인 클래스 | 실행 엔트리포인트, `package main` + `func main()` 필수 |
| `internal/` | 없음 (Go 고유 개념) | 컴파일러가 강제하는 "외부에서 import 금지" 폴더 |
| `interface` (예: `queue.Handler`) | Java `interface` | 거의 동일한 개념. 구조체가 메서드만 구현하면 자동으로 해당 인터페이스를 만족(암묵적 구현, `implements` 키워드 없음) |
| `struct` | `class` (필드만) | 메서드는 별도로 `func (c *Consumer) 메서드명(...)` 형태로 구조체 밖에 선언 |
| `error` 반환값 | `try/catch` 예외 | Go는 예외가 없고, 함수가 **마지막 반환값으로 error를 명시적으로 리턴**. 호출부에서 매번 `if err != nil` 체크 |
| `goroutine` (`go func(){...}`) | `Thread`/`@Async` | OS 스레드보다 훨씬 가벼운 동시성 단위. `consumer.go`에서 `go c.reclaimLoop(ctx)`로 백그라운드 루프 실행 |
| `context.Context` | 없음 (Java엔 대응 개념 약함) | 취소/타임아웃/그레이스풀 셧다운을 함수 인자로 명시적으로 전달하는 Go 표준 패턴. 이 프로젝트 전체에서 핵심적으로 쓰임 |
| `log/slog` | SLF4J/Logback | Go 1.21+ 표준 라이브러리 구조화 로깅. 이 프로젝트는 로깅 라이브러리를 별도로 안 쓰고 이것만 사용 |
| `defer` | `finally` | 함수 종료 시 실행 보장 (`defer rdb.Close()` 등) |

## 4. 두 저장소 간 계약(Contract) — 절대 잊으면 안 되는 것

* **메시지 필드**: `submissionId`, `problemId`, `userId`, `language`, `sourceCode`,
  `timeLimitSeconds`, `memoryLimitMb`, `submittedAt` — 8개 필드, 양쪽 다 문자열로 직렬화.
  한쪽만 필드를 바꾸면 파싱이 깨짐 (Go 쪽은 `ParseSubmissionMessage`가 즉시 poison message로
  처리해 dead-letter로 보냄).
* **스트림/그룹 이름**: `judge:submissions` (메인), `judge-workers` (컨슈머 그룹),
  `judge:submissions:dead-letter` (DLQ 역할). 두 저장소가 **서로 다른 이름의 환경변수**로
  이 값을 오버라이드하므로(Spring: `judge.queue.stream-key` ← `JUDGE_QUEUE_STREAM_KEY`,
  Go: 직접 `JUDGE_QUEUE_STREAM_KEY`), 기본값을 바꿀 땐 **양쪽 저장소를 모두** 확인해야 함.
* **이슈 #4의 DTO 스펙과 실제 구현이 다름**: 이슈에는 4개 필드만 있었지만 실제로는 4개가
  더 추가됨(Problem의 제한시간/메모리를 매번 DB 재조회하지 않기 위해). 이슈가 "discussion"
  라벨이라 필드를 또 바꿀 수 있으니, 바꾸기 전 이슈 코멘트로 먼저 합의할 것.
* **핸들러는 멱등(idempotent)이어야 함**: Redis Streams는 at-least-once라 같은 제출물이
  두 번 배달될 수 있음. 실제 채점 로직을 넣을 때 이 전제를 깨면 안 됨.

## 5. 앞으로 작업 순서 제안

1. **실제 채점 로직 구현** (`worker/internal/judge/handler.go`의 `StubHandler` 대체)
   - S3에서 테스트케이스 다운로드 (ADR-0002)
   - 언어별 컴파일 (`os/exec`로 `javac`/`gcc`/`python3` 등 외부 프로세스 실행)
   - 샌드박스 실행 (자원 제한, 격리) — 아래 6절 "가장 큰 공부 영역" 참고
   - 결과를 `POST /api/judge/submission/result`로 콜백
2. **Dockerfile 작성** (`worker/Dockerfile`) — 멀티스테이지 빌드로 정적 바이너리 생성
3. **스트림 트리밍 정책** 추가 (`XTRIM`/`MAXLEN`)
4. **Go 테스트 작성** (`consumer.go`의 3가지 에러 분기 등 우선)
5. **`debuging tool for window/` 정합성 갱신** (옛 `webController/` 경로 참조 정리)
6. 후순위: Redis 이중화(Sentinel/Cluster), 동시성 튜닝(워커 프로세스 수평 확장)

## 6. Go 학습 로드맵 (Go를 전혀 모른다는 전제)

우선순위 순서로 정리. 이 저장소 코드를 이해/확장하는 데 실제로 쓰이는 것만 추림.

### 6.1 Go 기본 문법 (필수, 가장 먼저)
* 패키지/임포트, `func`, 다중 반환값, **`error` 값 기반 에러 처리**(예외 없음 — 이게
  Java와 가장 다른 사고방식 전환 포인트)
* `struct`와 메서드(`func (c *Consumer) Run(...)`), 포인터 리시버 vs 값 리시버
- `interface`의 **암묵적 구현**(implements 키워드 없이, 메서드 시그니처만 맞으면 인터페이스 만족)
* `defer`, 제로값(zero value) 개념
* 참고: [A Tour of Go](https://go.dev/tour/) (공식 인터랙티브 튜토리얼, 반나절이면 기본기 끝남)

### 6.2 동시성 (goroutine, channel, context) — 이 프로젝트의 핵심
* goroutine이 뭔지, 왜 스레드보다 가벼운지
* `context.Context`: 취소(cancel)/타임아웃/데드라인을 함수 인자로 넘기는 패턴.
  `main.go`의 `signal.NotifyContext`(SIGINT/SIGTERM → context 취소로 그레이스풀 셧다운),
  `consumer.go` 전체가 이 패턴 위에서 동작함
* 참고: [Effective Go - Goroutines](https://go.dev/doc/effective_go#goroutines),
  [Go Concurrency Patterns: Context](https://go.dev/blog/context)

### 6.3 Go 프로젝트/도구 체계
* `go.mod`/`go.sum`, `go build`, `go vet`, `go test`, `internal/` 규칙,
  표준 프로젝트 레이아웃(`cmd/`, `internal/`) 관례
* 참고: [Standard Go Project Layout](https://github.com/golang-standards/project-layout)
  (강제 규칙은 아니지만 이 저장소가 그대로 따르고 있음)

### 6.4 표준 라이브러리 중 이 프로젝트가 쓰는 것
* `log/slog` (구조화 로깅)
* `os/exec` — **앞으로 컴파일러/인터프리터를 자식 프로세스로 실행할 때 필수**
* `os/signal`, `syscall` — 그레이스풀 셧다운
* Go의 테스트 프레임워크(`testing` 패키지, 테이블 기반 테스트 관례) — 아직 테스트가 없으니 곧 필요

### 6.5 Redis Streams 자체 (언어 무관하게 반드시 알아야 함)
Go 문법보다 이게 더 중요할 수 있음 — `docs/redis-stream-queue-design.md`를 먼저 정독하고,
모르는 명령어는 Redis 공식 문서로 확인:
* Consumer Group 개념, PEL(Pending Entries List)
* `XADD`/`XREADGROUP`/`XACK`/`XPENDING`/`XCLAIM`/`XAUTOCLAIM`
* 참고: [Redis Streams 공식 문서](https://redis.io/docs/latest/develop/data-types/streams/)

### 6.6 실제 채점 로직 구현 시 필요해질 것 (다음 단계 착수 전 학습)
* **`github.com/redis/go-redis/v9` 클라이언트 API** — 이미 쓰이고 있으니 소스 코드 읽으면서 익히면 됨
* **AWS S3 Go SDK** (`aws-sdk-go-v2`) — ADR-0002의 테스트케이스 다운로드 구현 시
* **샌드박스/격리 기술** — 이 프로젝트에서 가장 어려운 부분이 될 가능성이 높음. Go로 직접
  Linux `cgroups`/`namespaces`/`seccomp`를 다루거나(저수준, 난이도 높음), 기존 온라인 저지가
  쓰는 샌드박스 도구(`isolate`, `nsjail`, gVisor 등)를 Go에서 자식 프로세스로 호출하는 방식
  중 택일 필요 — 착수 전에 팀 내 논의 및 별도 ADR 권장
* **HTTP 클라이언트** (`net/http`) — 결과 콜백 POST용, Go 표준 라이브러리만으로 충분

### 6.7 참고 순서 요약
1. A Tour of Go (기본 문법, 반나절)
2. 이 저장소의 `worker/internal/queue/*.go` 3개 파일을 위 6.1~6.2 지식으로 다시 읽어보기
   (이미 작동하는 코드라서 최고의 학습 자료)
3. `docs/redis-stream-queue-design.md` 정독
4. `os/exec` + 테스트 작성법 학습 → `handler.go` 구현 착수
5. 샌드박스 기술 조사는 별도 시간 잡고 조사 후 팀과 방향 합의
