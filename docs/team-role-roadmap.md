# 채점 로직 구현 — 역할 분담 & 학습 로드맵 (최적화 / 보안)

`docs/go-onboarding.md`(구조 파악), `docs/go-redis-study-guide.md`(Go/Redis 학습 자료)를
바탕으로, 실제 채점 로직(`worker/internal/judge/handler.go`의 `StubHandler` 대체) 구현을
**최적화 담당 1명 + 보안 담당 1명**으로 나눠 진행하기 위한 역할 분담과 로드맵이다.
둘 다 Go로 채점 로직을 짜지만, **모듈 경계를 인터페이스로 명확히 나눠 코드가 겹치지 않게** 설계했다.

---

## 1. 겹치지 않게 나누는 방법: 인터페이스 경계선

채점 파이프라인을 하나의 인터페이스로 쪼개면, 두 사람이 **서로의 구현을 몰라도** 각자
작업할 수 있다.

```go
// internal/judge/executor 패키지 (보안 담당이 구현)
type Executor interface {
    // 격리된 환경에서 명령 하나를 실행하고 결과(stdout/stderr/exit code/실제 사용 시간·메모리)를 반환.
    // 자원 제한·타임아웃·격리는 이 안에서 전부 책임진다.
    Run(ctx context.Context, cmd Command, limits ResourceLimits) (*ExecResult, error)
}
```

```go
// internal/judge 패키지의 파이프라인 (최적화 담당이 구현)
// Executor 인터페이스만 알면 되고, 내부에서 cgroups/네임스페이스를 어떻게 쓰는지는 몰라도 됨.
func (p *Pipeline) Handle(ctx context.Context, msg *queue.SubmissionMessage) error {
    testcase := p.fetcher.Fetch(ctx, msg.ProblemID)     // 최적화 담당: S3 다운로드/캐싱
    result := p.executor.Run(ctx, buildCmd(msg), limits) // 보안 담당의 Executor 호출만 함
    p.callback.Report(ctx, msg.SubmissionID, result)     // 최적화 담당: 결과 콜백 HTTP
}
```

**이 인터페이스(`Executor`, `Command`, `ResourceLimits`, `ExecResult` 타입)만 회의에서
먼저 합의**해두면, 그 다음부터는 각자 폴더 안에서 독립적으로 작업 가능:

| 폴더/파일 | 담당 | 내용 |
|---|---|---|
| `internal/judge/executor/` (신규) | **보안 담당** | 샌드박스 실행기 — 여기가 메인 작업 공간 |
| `internal/judge/fetch/` (신규) | **최적화 담당** | S3 다운로드 + tmpfs 캐싱 |
| `internal/judge/callback/` (신규) | **최적화 담당** | 결과 콜백 HTTP 클라이언트 (재시도/타임아웃) |
| `internal/judge/handler.go` | **공동 소유** (오케스트레이션만, 로직 없음) | 위 세 조각을 순서대로 호출하는 얇은 코드 |
| `internal/queue/` (기존) | **최적화 담당** | 스트림 트리밍 등 큐 최적화 추가 시 |
| `worker/Dockerfile` (신규) | **보안 담당** | non-root, capability drop 등 하드닝 우선 (최적화 담당은 빌드 캐시/이미지 크기 피드백만) |
| Redis ACL/`requirepass` 설정 | **보안 담당** | `redis.conf`/운영 스크립트 레벨, Go 코드와 별개 |
| `internal/config/config.go`의 배치/타임아웃 튜닝 | **최적화 담당** | 기존 파일이지만 값 튜닝만 하므로 충돌 낮음 |

→ 실질적으로 **파일 단위로 겹치는 지점이 없다.** 유일하게 함께 정할 것은 `Executor`
인터페이스의 시그니처 — 이건 첫 회의에서 30분만 투자해서 합의하면 됨.

---

## 2. 최적화 담당 (백엔드 개발자) 로드맵

### 2.1 왜 이 사람이 이 영역인가
백엔드 개발자는 이미 Spring 쪽에서 I/O, 동시성, 큐 연동 감각이 있으므로, **채점 파이프라인의
처리량과 자원 재사용**을 다루는 쪽이 기존 역량과 가장 잘 맞는다.

### 2.2 공부할 것 (우선순위 순)
1. **Go 동시성 패턴**: goroutine, channel, worker pool (`go-redis-study-guide.md` 2.2절)
   → [Go Concurrency Patterns](https://go.dev/blog/pipelines)
2. **context.Context**: 타임아웃/취소 전파 — Executor 호출 시 반드시 필요
   → [Go blog: Context](https://go.dev/blog/context)
3. **AWS S3 Go SDK** (`aws-sdk-go-v2`): 테스트케이스 다운로드
   → [AWS SDK for Go v2 문서](https://aws.github.io/aws-sdk-go-v2/docs/)
4. **tmpfs 캐싱 개념** (ADR-0002가 이미 요구): 최초 1회 다운로드 후 메모리 속도로 접근
   → [Linux tmpfs 커널 문서](https://www.kernel.org/doc/html/latest/filesystems/tmpfs.html)
5. **Redis Streams 최적화**: `XTRIM`/`MAXLEN`, `XREADGROUP` COUNT/BLOCK 튜닝
   → `go-redis-study-guide.md` 4절, [XADD](https://redis.io/docs/latest/commands/xadd/)
6. **pprof 프로파일링 + 벤치마크 테스트** (`testing.B`)
   → [Profiling Go Programs](https://go.dev/blog/pprof)
7. **Docker 멀티스테이지 빌드** (이미지 경량화 관점)
   → [Docker multi-stage builds](https://docs.docker.com/build/building/multi-stage/)

### 2.3 구현 테스크
- [ ] `internal/judge/fetch`: S3에서 테스트케이스(Input/Output) 다운로드 클라이언트
- [ ] tmpfs 기반 로컬 캐싱 (문제 ID 기준, 최초 1회 다운로드 후 재사용)
- [ ] `internal/judge/callback`: 결과 콜백 HTTP 클라이언트 (`POST /api/judge/submission/result`,
      재시도/타임아웃/connection 재사용)
- [ ] `internal/queue`에 스트림 트리밍 로직 추가 (`XADD ... MAXLEN ~ N` 또는 별도 배치)
- [ ] `config.go`의 `BatchSize`/`BlockTimeout`/`ClaimInterval` 실측 기반 튜닝
- [ ] `worker/Dockerfile` 멀티스테이지 빌드 (보안 담당의 하드닝 요구사항 반영해서 같이 리뷰)
- [ ] pprof로 파이프라인 프로파일링 + 벤치마크 테스트 작성
- [ ] 여러 컨슈머 프로세스로 수평 확장 부하 테스트 (같은 그룹, 다른 컨슈머 이름)

---

## 3. 보안 담당 로드맵

### 3.1 왜 이 영역이 중요한가
채점 서버는 **신뢰할 수 없는 사용자 코드를 실제로 실행**하는 유일한 컴포넌트다. 이 프로젝트에서
가장 실패 비용이 큰 부분(호스트 침해, 다른 사용자 코드/데이터 유출)이 여기서 발생한다.

### 3.2 공부할 것 (우선순위 순)
1. **`os/exec` 안전한 사용법**: 셸을 거치지 않는 인자 배열 전달, 커맨드 인젝션 방지
   → [os/exec 공식 문서](https://pkg.go.dev/os/exec)
2. **프로세스 격리 기초**: `syscall.SysProcAttr`, 프로세스 그룹 kill (타임아웃 시 자식까지 전부 종료)
   → [Go syscall.SysProcAttr](https://pkg.go.dev/syscall#SysProcAttr)
3. **Linux 자원 제한**: cgroups v2 (CPU/메모리/PID 수 상한)
   → [cgroups v2 커널 문서](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html)
4. **Linux 네임스페이스**: mount/pid/net/user 네임스페이스로 파일시스템/프로세스/네트워크 격리
   → [Linux namespaces(7)](https://man7.org/linux/man-pages/man7/namespaces.7.html)
5. **기존 샌드박스 도구 조사 및 선택**: isolate / nsjail / gVisor 비교, 하나 골라 로컬에서 직접 실행해보기
   → [isolate](https://github.com/ioi/isolate), [nsjail](https://github.com/google/nsjail)
6. **seccomp 개념** (화이트리스트 밖 syscall 차단) — 최소한 개념만, 직접 구현보다 기존 도구 활용 우선
   → [seccomp(2)](https://man7.org/linux/man-pages/man2/seccomp.2.html)
7. **Redis 보안**: ACL 최소 권한 분리, `requirepass`, 위험 명령어 제한
   → [Redis ACL 공식 문서](https://redis.io/docs/latest/operate/oss_and_stack/management/security/acl/)
8. **Docker 컨테이너 하드닝**: non-root 유저, capability drop, read-only 파일시스템
   → [Docker security 공식 문서](https://docs.docker.com/engine/security/)
9. **govulncheck**: 의존성 취약점 점검 습관화
   → [Go blog: govulncheck](https://go.dev/blog/vuln)

### 3.3 구현 테스크
- [ ] `internal/judge/executor`: `Executor` 인터페이스 구현 — 자원 제한 + 격리 + 타임아웃 강제 kill
- [ ] 언어별 컴파일/실행 커맨드의 안전한 조립 (`language` 화이트리스트 검증, 인자 배열 조립)
- [ ] 작업 디렉토리 격리 + 경로 순회 방지 (`filepath.Clean`, 임시 디렉토리 정리)
- [ ] isolate(또는 선택한 도구)를 Go에서 `os/exec`로 감싸는 wrapper 구현 및 검증
- [ ] Redis ACL 설정: producer는 `XADD`/`XGROUP CREATE`만, consumer는 `XREADGROUP`/`XACK`/
      `XCLAIM`/`XAUTOCLAIM`/dead-letter `XADD`만 허용하도록 최소 권한 분리
- [ ] `worker/Dockerfile` 하드닝 (non-root 유저, 불필요 바이너리 제거)
- [ ] 악성/포이즌 입력 테스트: 초과 크기 `sourceCode`, 잘못된 `language`, 무한루프/포크폭탄
      시나리오를 Executor 격리 안에서 실행해 실제로 격리되는지 검증
- [ ] 결과 콜백 요청의 위·변조 방지 검토 (백엔드 팀과 협의 — 워커→백엔드 콜백 인증 방식)

---

## 4. 주차별 로드맵 (학습 + 구현 병행)

두 사람 다 "학습만 하고 나중에 구현"이 아니라 **매주 학습과 구현을 병행**하는 것을 권장한다
(Go는 튜토리얼보다 실제 코드를 읽고 고치면서 느는 속도가 훨씬 빠름).

| 주차 | 최적화 담당 | 보안 담당 | 공동 |
|---|---|---|---|
| **1주차** | Go 동시성/context 학습 + `fetch` 패키지 골격 작성 | `os/exec`/cgroups/네임스페이스 학습 + isolate/nsjail 로컬 실습 | 회의에서 `Executor` 인터페이스 시그니처 합의 |
| **2주차** | S3 다운로드 + tmpfs 캐싱 구현, 유닛 테스트 | `Executor` 구현 착수 (자원 제한 + 격리, 우선 단일 언어로 PoC) | 중간 체크: Executor PoC로 파이프라인 연결 테스트 |
| **3주차** | 결과 콜백 클라이언트 + 스트림 트리밍 구현 | 언어별 컴파일/실행 커맨드 조립 + 악성 입력 테스트 작성 | `handler.go` 오케스트레이션 코드 함께 리뷰 |
| **4주차** | pprof 프로파일링, 부하 테스트, Dockerfile 이미지 최적화 | Redis ACL 적용, Dockerfile 하드닝, govulncheck 점검 | 통합 테스트 (전체 채점 플로우 end-to-end), ADR 작성(샌드박스 도구 선택 근거 등) |

> 트래픽/일정에 따라 주차는 늘어날 수 있음. 핵심은 **1주차에 인터페이스를 먼저 고정**하는
> 것 — 이게 늦어지면 이후 병렬 작업이 막힌다.

---

## 5. 회의에서 반드시 정하고 가야 할 것

1. `Executor` 인터페이스 시그니처 (입력: 명령/자원제한, 출력: 실행결과) — 위 1절 초안 기준으로 확정
2. 샌드박스 도구 선택 여부 (isolate vs nsjail vs 직접 구현) — 보안 담당이 후보 조사해오고,
   결정되면 반드시 새 ADR로 기록 (`docs/adr/` 네이밍 규칙 참고: `NNNN-영문-소문자-케밥.md`)
3. 언어 지원 우선순위 (JAVA/PYTHON3/CPP/C 중 어떤 언어부터 PoC를 만들지)
4. 두 모듈(Executor ↔ 나머지 파이프라인) 연결 시점 — 각자 스텁으로 먼저 개발하고 2주차에
   합치는지, 처음부터 실제 구현으로 붙일지
