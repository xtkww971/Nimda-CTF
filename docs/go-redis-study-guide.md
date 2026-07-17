# Go × Redis 학습 자료 (보안 / 최적화 관점)

`docs/go-onboarding.md`가 "이 저장소가 어떻게 생겼는지"에 대한 지도였다면, 이 문서는
"Go와 Redis를 이 프로젝트에 쓰기 위해 무엇을, 왜 공부해야 하는지"에 집중한 학습 자료다.
채점 서버는 **① 신뢰할 수 없는 사용자 코드를 실행**하고 **② 대량의 채점 요청을 큐로
처리**하는 시스템이라서, 일반적인 웹 서비스보다 보안과 최적화가 훨씬 직접적으로
아키텍처에 영향을 준다. 그래서 항목마다 "이 프로젝트에서 왜 중요한가"를 먼저 적었다.

---

## 1. Go — 보안(Security) 관점

### 1.1 왜 이 프로젝트에서 Go 보안이 특히 중요한가
다른 CRUD 서버와 달리 이 워커는 **사용자가 제출한 소스 코드를 컴파일하고 실행**한다.
즉 "공격자가 임의 코드 실행 권한을 이미 갖고 시작하는" 워크로드다. 목표는 그 코드가
호스트 시스템(다른 사용자 파일, 네트워크, 커널)에 영향을 주지 못하게 **격리**하는 것.

### 1.2 핵심 주제

| 주제 | 왜 필요한가 | 참고 자료 |
|---|---|---|
| `os/exec`로 외부 프로세스 실행 시 주의점 | 컴파일러/인터프리터를 자식 프로세스로 띄울 때, **셸을 거치지 않고** 인자를 배열로 넘겨야 커맨드 인젝션을 막을 수 있음 (`exec.Command("sh","-c", userInput)` 금지) | [os/exec 공식 문서](https://pkg.go.dev/os/exec) |
| 자원 제한 (rlimit, cgroups) | 사용자 코드가 무한루프/포크폭탄/메모리 폭주를 일으켜도 호스트에 영향 없게 CPU/메모리/프로세스 수 제한 필요. Go에서 `syscall.SysProcAttr`로 프로세스 그룹을 분리하고, 리눅스 cgroups v2로 자원 상한을 건 뒤 그 안에서 자식 프로세스 실행 | [cgroups v2 커널 문서](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html), [Go syscall.SysProcAttr](https://pkg.go.dev/syscall#SysProcAttr) |
| 네임스페이스 격리 (mount/pid/net/user) | 사용자 코드가 호스트 파일시스템/다른 프로세스/네트워크를 보지 못하게 리눅스 네임스페이스로 분리. Go 표준 라이브러리는 이걸 직접 안 해주므로, `syscall.SysProcAttr.Cloneflags`로 직접 설정하거나 기존 샌드박스 도구를 감싸는 방식 중 선택해야 함 | [Linux namespaces(7)](https://man7.org/linux/man-pages/man7/namespaces.7.html) |
| seccomp (시스템콜 화이트리스트) | 컴파일된 사용자 바이너리가 위험한 syscall(예: `ptrace`, 임의 `mount`)을 못 쓰게 필터링. 직접 구현은 복잡하므로 기존 도구(`isolate`, `nsjail`, gVisor) 활용을 우선 검토 권장 | [seccomp(2)](https://man7.org/linux/man-pages/man2/seccomp.2.html) |
| 타임아웃 강제 종료 | `context.WithTimeout` + `cmd.Process.Kill()`로 시간 제한 초과 시 반드시 프로세스 트리 전체를 죽여야 함(자식이 더 자식을 낳는 경우 고아 프로세스로 남을 수 있음 — 프로세스 그룹 kill 필요) | [context 공식 문서](https://pkg.go.dev/context), [Go blog: Context](https://go.dev/blog/context) |
| 입력 검증 | `SubmissionMessage`의 `language` 필드가 화이트리스트(JAVA/PYTHON3/CPP/C) 밖의 값이면 즉시 거부. 파일 경로 조립 시 `filepath.Clean` 없이 사용자 입력을 경로에 그대로 넣지 않기(경로 순회 방지) | [path/filepath 공식 문서](https://pkg.go.dev/path/filepath) |
| 시크릿 관리 | Redis 비밀번호, S3 키를 코드/로그에 남기지 않기. 환경변수로만 주입(`config.go`가 이미 이 패턴을 씀), `log/slog`로 로깅할 때 민감 필드 마스킹 | [OWASP Secrets Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html) |
| 의존성 취약점 점검 | `go.sum`에 고정된 의존성(`go-redis` 등)의 CVE를 주기적으로 점검 | [govulncheck](https://go.dev/blog/vuln), `go run golang.org/x/vuln/cmd/govulncheck@latest` |

### 1.3 실전 옵션: 직접 구현 vs 기존 도구
샌드박스를 처음부터 syscall로 직접 짜는 것은 난이도가 매우 높다. 실무에서 흔한 선택지:
* **isolate** (Codeforces/BOJ 계열이 실제로 쓰는 오픈소스 채점 샌드박스, C로 작성) — Go에서
  `os/exec`로 감싸서 호출
* **nsjail** (Google 제작, 프로세스 격리 전용)
* **gVisor** (Google, 유저스페이스 커널 — 격리 수준은 높지만 오버헤드도 있음)
* **Firecracker microVM** (AWS, VM 수준 격리 — 가장 강력하지만 인프라 복잡도 최고)

→ 처음부터 직접 만들기보다 **isolate를 Go에서 프로세스로 감싸는 방식**부터 검토하고,
결정하면 반드시 ADR로 남길 것 (ADR-0002가 참고한 tmpfs 캐싱과도 연결되는 결정임).

---

## 2. Go — 최적화(Performance) 관점

### 2.1 왜 이 프로젝트에서 Go 최적화가 특히 중요한가
`docs/redis-stream-queue-design.md`에 이미 명시된 전략은 "워커 내부 동시성보다 **워커
프로세스 수를 늘리는 수평 확장**"이다. 즉 단일 워커 프로세스의 처리량보다는, **프로세스
하나가 얼마나 가볍고 빠르게 뜨는지**, 그리고 **채점 한 건당 리소스를 얼마나 깨끗하게
반환하는지**가 더 중요하다.

### 2.2 핵심 주제

| 주제 | 왜 필요한가 | 참고 자료 |
|---|---|---|
| goroutine/channel 활용 패턴 | 지금은 순차 처리(`Consumer.Run`이 메시지를 하나씩 처리)지만, 향후 I/O 대기(S3 다운로드 등)를 goroutine으로 병렬화할 여지가 있음. 다만 과도한 동시성은 컴파일/샌드박스 자원 경합을 유발하므로 **worker pool 패턴**(고정 개수 goroutine + 채널)으로 상한을 둬야 함 | [Go Concurrency Patterns](https://go.dev/blog/pipelines), [Effective Go - Goroutines](https://go.dev/doc/effective_go#goroutines) |
| 메모리 할당 최소화 | `sourceCode`처럼 큰 문자열을 다루는 코드에서 불필요한 문자열 복사/연결(`+=`) 대신 `strings.Builder` 사용. Go의 GC는 자동이지만 할당이 많을수록 GC 압박 증가 | [strings.Builder](https://pkg.go.dev/strings#Builder) |
| pprof로 프로파일링 | "느리다"를 추측하지 않고 실측하기 위한 표준 도구. CPU/메모리 프로파일을 뜨고 핫스팟 확인 | [Profiling Go Programs](https://go.dev/blog/pprof), `net/http/pprof` |
| 정적 바이너리 & 빌드 최적화 | ADR-0003이 강조한 Go의 장점(단일 정적 바이너리, 낮은 런타임 오버헤드)을 실제로 살리려면 Dockerfile을 멀티스테이지로 작성해 최종 이미지에 Go 런타임 빌드 도구 없이 바이너리만 남기기 | [Docker multi-stage builds](https://docs.docker.com/build/building/multi-stage/) |
| `GOMAXPROCS`와 컨테이너 자원 | 컨테이너의 CPU 제한(cgroup)을 Go 런타임이 자동으로 인식 못 하던 문제가 있었음(최신 Go는 개선됨). 컨테이너 환경에서 CPU 할당량과 `GOMAXPROCS`가 안 맞으면 스케줄링 낭비 발생 | [Go runtime: GOMAXPROCS](https://pkg.go.dev/runtime#GOMAXPROCS), [uber-go/automaxprocs](https://github.com/uber-go/automaxprocs) |
| I/O 버퍼링 | S3 다운로드/로컬 파일 쓰기 시 `bufio.Reader`/`bufio.Writer`로 시스템콜 횟수 줄이기 | [bufio 공식 문서](https://pkg.go.dev/bufio) |
| 커넥션 재사용 | `redis.Client`, S3 클라이언트는 매 요청마다 새로 만들지 말고 워커 생명주기 동안 하나만 유지(이미 `main.go`가 이렇게 하고 있음 — 이 패턴을 유지) | [go-redis 공식 문서](https://redis.uptrace.dev/) |

---

## 3. Redis — 보안(Security) 관점

### 3.1 왜 이 프로젝트에서 Redis 보안이 특히 중요한가
큐 메시지 안에 **사용자의 소스 코드 원문**이 그대로 담긴다 (`sourceCode` 필드, 평문).
Redis는 기본 설정이 인증 없이 열려 있는 경우가 많아서, 설정 실수 하나가 바로 "사용자
코드 유출" 사고로 이어질 수 있는 컴포넌트다.

### 3.2 핵심 주제

| 주제 | 왜 필요한가 | 참고 자료 |
|---|---|---|
| 인증 (`requirepass` / ACL) | Redis 기본 설정은 무인증 접속을 허용할 수 있음. 최소한 `requirepass`, 가능하면 **ACL**로 producer는 `XADD`/`XGROUP`만, consumer는 `XREADGROUP`/`XACK`/`XCLAIM`/`XAUTOCLAIM`/`XADD`(dead-letter용)만 허용하는 최소 권한 원칙 적용 | [Redis ACL 공식 문서](https://redis.io/docs/latest/operate/oss_and_stack/management/security/acl/) |
| 네트워크 노출 최소화 | Redis 포트를 외부에 직접 열지 않고 내부 네트워크(docker-compose의 `nimda-network`처럼)에서만 접근 가능하게. `bind` 설정으로 리스닝 인터페이스 제한 | [Redis Security 공식 문서](https://redis.io/docs/latest/operate/oss_and_stack/management/security/) |
| 전송 구간 암호화 (TLS) | 소스 코드가 평문으로 네트워크를 타므로, 워커와 Redis가 물리적으로 분리된 환경(다른 서버/리전)이라면 TLS 필수. 같은 docker 네트워크 내부라면 상대적으로 낮은 우선순위지만 운영 환경 전환 시 재검토 | [Redis TLS 공식 문서](https://redis.io/docs/latest/operate/oss_and_stack/management/security/encryption/) |
| 위험한 명령어 제한 | `FLUSHALL`, `CONFIG`, `KEYS` 같은 명령어를 운영 환경에서 `rename-command`나 ACL로 막아 실수/악용으로 인한 전체 데이터 삭제 방지 | [redis.conf 예제](https://redis.io/docs/latest/operate/oss_and_stack/management/config-file/) |
| Dead-letter 스트림 접근 통제 | 실패한 제출물(소스 코드 포함)이 쌓이는 곳이라 별도 스트림이라고 방심하면 안 됨 — 메인 스트림과 동일한 접근 통제 필요 | `docs/redis-stream-queue-design.md` 2.3절 |
| 민감정보 로깅 금지 | `Consumer.process`가 에러 로그를 찍을 때 `sourceCode` 전체를 로그에 남기지 않도록 주의(현재 코드는 `submissionId` 등 식별자만 로깅하고 있어 이 원칙을 따르고 있음 — 앞으로 로그 추가 시에도 유지) | [OWASP Logging Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html) |

---

## 4. Redis — 최적화(Performance) 관점

### 4.1 왜 이 프로젝트에서 Redis 최적화가 특히 중요한가
Redis Streams는 **ACK된 엔트리도 자동 삭제되지 않는다** (이미 설계 문서 4절에 한계로
명시됨). 트래픽이 늘어날수록 메모리 사용량이 무한정 늘어날 수 있어서, "일단 동작하게"와
"운영 가능하게"의 격차가 가장 크게 벌어지는 지점이다.

### 4.2 핵심 주제

| 주제 | 왜 필요한가 | 참고 자료 |
|---|---|---|
| 스트림 트리밍 (`XTRIM` / `MAXLEN`) | 이미 이 저장소의 "아직 안 된 것" 1순위 후속 과제. `XADD ... MAXLEN ~ N`으로 근사치 트리밍하면 정확한 트리밍보다 훨씬 저렴 (`~` 옵션이 핵심) | [XADD 공식 문서](https://redis.io/docs/latest/commands/xadd/), [XTRIM 공식 문서](https://redis.io/docs/latest/commands/xtrim/) |
| `COUNT`/`BLOCK` 튜닝 (`XREADGROUP`) | 현재 `BatchSize=10`, `BlockTimeout=5s`(`config.go`). COUNT를 너무 크게 하면 한 워커가 메시지를 독점해 다른 워커가 굶을 수 있고, 너무 작으면 왕복(round-trip) 비용 증가. 트래픽 패턴 보고 조정 필요 | [XREADGROUP 공식 문서](https://redis.io/docs/latest/commands/xreadgroup/) |
| `XAUTOCLAIM`의 `MinIdleTime`/`ClaimInterval` 튜닝 | 너무 짧으면 아직 처리 중인 정상 메시지를 성급하게 재할당(중복 처리 증가), 너무 길면 크래시 복구가 느려짐. 채점 작업의 실제 최대 소요 시간(컴파일+샌드박스 실행)보다 여유 있게 설정해야 함 | [XAUTOCLAIM 공식 문서](https://redis.io/docs/latest/commands/xautoclaim/) |
| Consumer Group 수평 확장 | 컨슈머 이름만 다르게 해서 같은 그룹에 워커 프로세스를 늘리면 자동으로 메시지가 분산됨(이미 설계에 반영됨) — 처리량이 부족하면 워커 내부 최적화보다 **프로세스 replica 수 증설**이 우선 대응 | `docs/redis-stream-queue-design.md` 4절 |
| 파이프라이닝 | 여러 Redis 명령을 왕복 없이 한 번에 보내는 기법. 지금은 명령이 대부분 단건이라 우선순위는 낮지만, dead-letter 이관처럼 `XADD`+`XACK`을 묶어 처리할 때 검토 가능 | [Redis Pipelining 공식 문서](https://redis.io/docs/latest/develop/use/pipelining/) |
| 메모리 사용량 모니터링 | `MEMORY USAGE`, `INFO memory`로 스트림 크기 추이를 관찰. 트리밍 정책이 제대로 동작하는지 검증하는 유일한 방법 | [Redis INFO 공식 문서](https://redis.io/docs/latest/commands/info/) |
| Redis 이중화 (HA) | 단일 인스턴스라 장애 시 큐 전체 정지 — Sentinel(자동 failover) 또는 Cluster(샤딩) 도입은 트래픽 규모가 커질 때 검토 (현재는 후속 과제로만 문서화) | [Redis Sentinel 공식 문서](https://redis.io/docs/latest/operate/oss_and_stack/management/sentinel/) |

---

## 5. 학습 순서 제안

1. **Go 기본 문법** (A Tour of Go) — `go-onboarding.md` 6.1절 참고, 여기서는 생략
2. **Redis Streams 기본 개념 + 이 프로젝트의 큐 설계 문서** 정독
   (`docs/redis-stream-queue-design.md`) — 이미 왜 이렇게 만들었는지 근거가 다 있음
3. **위 표의 "★ 지금 당장 관련된 것"부터**: `os/exec` 안전한 사용법, 자원 제한 개념,
   `XTRIM`/`MAXLEN`, Consumer Group 접근 통제(ACL) — 다음 스프린트의 실제 구현과 직결
4. **샌드박스 기술 조사**는 따로 시간 잡고, isolate/nsjail 중 하나를 골라 로컬에서 직접
   실행해보는 것을 권장 (문서만 읽는 것보다 훨씬 빠르게 감이 잡히는 영역)
5. **최적화는 나중**: 실제 채점 로직이 없는 지금은 pprof/트리밍 튜닝보다 "정확히 동작하는
   것"이 우선. 최적화 항목들은 트래픽이 실제로 발생한 뒤 프로파일링 기반으로 접근할 것
   (추측 기반 최적화 금지 — Go 커뮤니티의 대표적인 격언: "measure, don't guess")

## 6. 종합 참고 자료 목록

* [A Tour of Go](https://go.dev/tour/)
* [Effective Go](https://go.dev/doc/effective_go)
* [Go 공식 블로그 - Context](https://go.dev/blog/context)
* [Go 공식 블로그 - pprof](https://go.dev/blog/pprof)
* [Go 공식 블로그 - govulncheck](https://go.dev/blog/vuln)
* [Redis Streams 공식 문서](https://redis.io/docs/latest/develop/data-types/streams/)
* [Redis Security 공식 문서](https://redis.io/docs/latest/operate/oss_and_stack/management/security/)
* [Redis ACL 공식 문서](https://redis.io/docs/latest/operate/oss_and_stack/management/security/acl/)
* [OWASP Cheat Sheet Series](https://cheatsheetseries.owasp.org/) (Secrets Management, Logging 등)
* [isolate (샌드박스)](https://github.com/ioi/isolate)
* [nsjail (샌드박스)](https://github.com/google/nsjail)
* [Linux namespaces(7)](https://man7.org/linux/man-pages/man7/namespaces.7.html)
* [cgroups v2 커널 문서](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html)
