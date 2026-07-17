# DX: 디버깅 / 테스트 / 프로파일링 가이드

이 문서는 `worker/`(Go)를 개발하면서 **어떻게 테스트하고, 어떻게 디버깅하고, 어떻게 수치를
개선할지**를 정리한 것이다. 문서만이 아니라 실제로 프로젝트에 도구를 추가해뒀으니, 아래
표부터 먼저 훑어보고 필요한 절로 이동하면 된다.

## 0. 이번에 추가된 것 한눈에 보기

| 파일/도구 | 역할 |
|---|---|
| `go.work` (저장소 루트) | VSCode에서 **`Judge Server` 폴더 자체를 워크스페이스로 열었을 때** gopls가 `worker/` 모듈을 제대로 인식하게 함 |
| `.vscode/launch.json` | VSCode에서 브레이크포인트 디버깅 3종 (워커 실행/테스트/attach) |
| `worker/internal/queue/message_test.go`, `backoff_test.go`, `worker/internal/config/config_test.go` | 테이블 기반 유닛 테스트 예시 (앞으로 테스트 작성 시 이 패턴을 따르면 됨) |
| `worker/internal/queue/message_test.go`의 `BenchmarkParseSubmissionMessage` | 벤치마크 작성 예시 |
| `worker/.golangci.yml` | 린트 설정 (gosec 포함 — 보안 관점 린트) |
| `worker/scripts/*.bat` | 테스트/벤치마크/린트/로컬 Redis 기동을 원클릭으로 |
| `worker/docker-compose.test.yml` | 웹 백엔드 없이 워커만 독립적으로 디버깅할 때 쓰는 격리된 Redis (포트 6380) |
| `worker/.env.example` | 로컬 디버깅용 환경변수 예시 (복사해서 `.env`로 사용, gitignore됨) |
| `main.go`의 `LOG_LEVEL` 지원 | 재배포 없이 로그 상세도 조절 |
| `main.go`의 `PPROF_ADDR` 지원 | 필요할 때만 pprof 프로파일링 서버 기동 |
| `.github/workflows/worker-ci.yml` | PR/push마다 build+vet+test(-race)+lint 자동 실행 |

---

## 1. 사전 준비: VSCode 워크스페이스 루트

VSCode에서 **`Nimda`나 `worker/`가 아니라 `Judge Server` 폴더 자체**를 열어야 위 도구들이
전부 맞물려 동작한다 (`go.work`가 이 폴더 기준으로 `./worker`를 가리키기 때문).
`worker/`만 열어도 Go 코드 자체는 동작하지만, `.vscode/launch.json`을 못 찾는다.

Go 확장(`golang.go`)이 설치돼 있어야 하며, 처음 디버그 실행 시 VSCode가 Delve(`dlv`)를
자동으로 설치할지 물어본다 — 설치를 허용하면 된다.

---

## 2. 테스트

### 2.1 실행 방법
```
worker/scripts/test.bat          # go test ./... -cover
worker/scripts/cover.bat         # 커버리지 HTML 리포트를 만들어 브라우저로 염
worker/scripts/bench.bat         # 벤치마크만 실행 (-run=^$ -bench=. -benchmem)
```
스크립트 없이 직접 실행해도 된다 (`worker/` 안에서):
```
go test ./...
go test ./... -v                 # 각 서브테스트 이름까지 출력
go test ./internal/queue/...     # 특정 패키지만
```

### 2.2 `-race`(경쟁 상태 탐지)는 왜 스크립트에 없나
Go의 race detector는 **cgo(C 컴파일러)** 가 필요하다. 이 저장소를 작업하는 대부분의
Windows 개발 환경엔 별도 C 툴체인(MSYS2/mingw-w64 등)이 없을 가능성이 높아, 로컬
기본 스크립트에서는 뺐다. 대신 **CI(`worker-ci.yml`)는 Ubuntu 러너에서 `-race`를 항상
실행**하므로, PR을 올리면 자동으로 검증된다. C 툴체인이 이미 있다면 로컬에서도
`go test ./... -race -cover`를 직접 돌려도 된다.

이 프로젝트는 `Consumer.Run`(메인 루프)과 `reclaimLoop`(백그라운드 goroutine)이 동시에
같은 `Consumer` 필드를 다루므로, 앞으로 필드를 추가할 때 race 여부를 CI에서 꼭 확인할 것.

### 2.3 테스트 작성 패턴 (테이블 기반 테스트)
Go 커뮤니티의 표준 관례인 **테이블 기반 테스트**를 이미 3개 파일에 적용해뒀다. 새 로직을
추가할 때 이 형태를 그대로 따라가면 된다.

```go
tests := []struct {
    name    string
    mutate  func(map[string]interface{})
    wantErr bool
}{
    {name: "valid message parses all fields", mutate: func(m map[string]interface{}) {}, wantErr: false},
    {name: "missing required field is rejected", mutate: func(m map[string]interface{}) { delete(m, "sourceCode") }, wantErr: true},
    // ...
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) { /* ... */ })
}
```
실제 예시: `worker/internal/queue/message_test.go` (정상/누락/빈 문자열/타입 불일치/파싱
실패 6가지 케이스), `worker/internal/config/config_test.go`(`t.Setenv`로 환경변수
오버라이드를 테스트하는 패턴 — 각 테스트가 서로 영향을 주지 않도록 Go 1.17+의
`t.Setenv`를 씀, 수동으로 `os.Setenv`+`defer os.Unsetenv` 할 필요 없음).

**앞으로 `judge.Handler`(실제 채점 로직) 구현 시 우선순위:**
* 보안 담당: 악성 입력(초과 크기 입력, 잘못된 language, 커맨드 인젝션 시도 문자열)이
  `Executor`에서 실제로 거부/격리되는지 검증하는 테스트
* 최적화 담당: S3 다운로드/캐싱 로직에 대해 "캐시 히트 시 네트워크 호출이 없는지" 같은
  동작 검증 + 아래 4절의 벤치마크

### 2.4 로컬 Redis가 필요한 테스트 (통합 테스트)
지금 추가한 유닛 테스트는 전부 Redis 없이 돈다 (순수 함수/파싱/설정 로딩만 검증).
하지만 `Consumer`(XREADGROUP/XACK/XAUTOCLAIM)를 실제로 검증하려면 진짜 Redis가 필요하다
— `docs/agent-handoff.md`에 기록된 3가지 시나리오(정상 처리, poison message, 워커 크래시
시뮬레이션)는 지금까지 **수동으로** `redis-cli`를 이용해 검증했다.

로컬에서 웹 백엔드 없이 워커만 독립적으로 띄우고 싶을 때:
```
worker/scripts/redis-up.bat      # 격리된 Redis, localhost:6380 (nimda-redis-dev와 별개)
# .env에 REDIS_ADDR=localhost:6380 설정 후 워커 실행/디버깅
worker/scripts/redis-down.bat    # 종료
```
> 다음 단계 제안: 이 수동 검증 3가지 시나리오를 Go 테스트로 자동화하려면 `testcontainers-go`
> (Docker로 Redis를 테스트 시작 시 띄웠다 종료하는 라이브러리)를 검토할 만하다. 지금은
> 범위 밖이라 도구만 준비해뒀다 (`docker-compose.test.yml`).

---

## 3. 디버깅

### 3.1 VSCode 브레이크포인트 디버깅
`.vscode/launch.json`에 3가지 구성을 넣어뒀다 (VSCode 왼쪽 "Run and Debug" 패널에서 선택):

* **Debug Worker (local)** — `cmd/worker`를 그대로 실행하며 브레이크포인트를 걸 수 있음.
  `worker/.env.example`을 복사해 `worker/.env`를 만들면 자동으로 로드된다
  (`REDIS_ADDR=localhost:6380`으로 맞춰서 `redis-up.bat`으로 띄운 격리 Redis를 보게 해뒀음).
* **Debug Current Package Tests** — 현재 열려 있는 `_test.go` 파일이 속한 패키지의
  테스트를 디버그 모드로 실행. 테스트 안에 브레이크포인트를 찍고 `왜 이 케이스가
  실패하는지` 한 줄씩 확인할 때 유용.
* **Attach to Running Worker (dlv)** — 이미 실행 중인(로컬 or 원격) 워커 프로세스에
  붙어서 디버깅. 프로세스 목록에서 고르면 됨.

### 3.2 VSCode 없이 CLI로 (Delve 직접 사용)
```
go install github.com/go-delve/delve/cmd/dlv@latest
cd worker
dlv debug ./cmd/worker              # 브레이크포인트: break internal/queue.(*Consumer).process
dlv test ./internal/queue           # 테스트를 디버그 모드로
```
Delve 기본 명령: `break <위치>`, `continue`, `next`(다음 줄), `step`(함수 안으로),
`print <변수>`, `goroutines`(현재 goroutine 목록 — 이 워커는 `reclaimLoop`이 별도
goroutine으로 도니, 크래시 재현할 때 특히 유용).

### 3.3 로그로 디버깅하기 (`LOG_LEVEL`)
`main.go`가 이제 `LOG_LEVEL` 환경변수를 읽는다 (`debug`/`info`/`warn`/`error`, 기본값
`info`). 배포를 다시 안 해도 되고, `.env`에 `LOG_LEVEL=debug`만 넣으면 됨. 참고: 지금은
`Info`/`Warn`/`Error` 레벨 로그만 있고 `Debug` 레벨 로그는 아직 없으므로, 실제 채점 로직을
구현하면서 세밀한 추적이 필요한 지점(예: S3 다운로드 진행 상황, Executor에 넘기는 최종
커맨드)에 `logger.Debug(...)`를 적극적으로 추가하는 걸 권장.

### 3.4 Redis 자체를 들여다보며 디버깅하기
`redis-cli`로 큐 상태를 직접 확인하는 게 Go 디버거보다 빠를 때가 많다:
```
redis-cli -p 6380 XRANGE judge:submissions - +                       # 전체 엔트리
redis-cli -p 6380 XPENDING judge:submissions judge-workers            # 미처리(PEL) 목록
redis-cli -p 6380 XPENDING judge:submissions judge-workers - + 10      # 상세 (재전달 횟수 포함)
redis-cli -p 6380 XRANGE judge:submissions:dead-letter - +            # dead-letter 확인
```
`docs/agent-handoff.md`에 기록된 "크래시 컨슈머 흉내" 검증(메시지를 읽고 일부러 ACK 안 함)도
이 명령어들로 직접 재현/관찰한 것이다 — 새 장애 시나리오를 검증할 때도 같은 방식을 쓰면 된다.

---

## 4. 프로파일링 & 수치 개선 (주로 최적화 담당)

### 4.1 벤치마크
```
worker/scripts/bench.bat
```
예시로 넣어둔 `BenchmarkParseSubmissionMessage`를 실행하면 이렇게 나온다:
```
BenchmarkParseSubmissionMessage-8   5112393   308.7 ns/op   96 B/op   1 allocs/op
```
읽는 법: 초당 처리량이 아니라 **"1회 호출당" 비용**이다 — `308.7 ns/op`(연산 1번에 걸린
시간), `96 B/op`(할당된 바이트), `1 allocs/op`(할당 횟수). 최적화 작업 전후로 이 숫자를
비교하면 "느낌"이 아니라 실측으로 개선을 증명할 수 있다. 새 로직(S3 다운로드, 캐싱 등)을
넣을 때 핫패스가 될 만한 함수에는 벤치마크를 같이 추가할 것.

### 4.2 pprof (CPU/메모리 프로파일링)
`PPROF_ADDR` 환경변수를 설정하면(`.env.example`에 예시 있음) `net/http/pprof`가 기동된다.
**로컬 디버깅용으로만 켤 것 — `localhost` 이외 주소로 열면 안 됨** (인증 없이 스택
트레이스/메모리 내용을 노출하는 엔드포인트라 `go-redis-study-guide.md` 1.2절의 보안
항목과 직결됨).
```
# .env에 PPROF_ADDR=localhost:6060 설정 후 워커 실행
go tool pprof http://localhost:6060/debug/pprof/heap        # 현재 메모리 스냅샷
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30   # 30초간 CPU 프로파일
```
pprof 대화형 셸에서 `top`(가장 비용 큰 함수), `web`(그래프, graphviz 필요), `list <함수명>`
(라인별 비용)으로 분석한다.

### 4.3 린트 (`golangci-lint`, gosec 포함)
```
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
worker/scripts/lint.bat        # go vet + (설치돼 있으면) golangci-lint
```
`worker/.golangci.yml`에 `gosec`을 넣어둔 이유: 이 워커는 곧 `os/exec`로 사용자 코드를
직접 실행하게 되는데, gosec이 커맨드 인젝션 패턴이나 안전하지 않은 파일 권한 같은 것을
정적 분석으로 미리 잡아준다. **보안 담당이 `Executor` 구현할 때 이 린트를 CI 게이트로
가장 먼저 활용하게 될 것.**

### 4.4 CI (`worker-ci.yml`)
`worker/**` 경로에 변경이 생기면 GitHub Actions가 자동으로 `go build` → `go vet` →
`go test -race -cover` → `golangci-lint`를 순서대로 돈다. PR 리뷰 전에 이 4가지가 이미
통과했다는 뜻이므로, 사람 리뷰는 로직/설계에 집중할 수 있다.

---

## 5. 앞으로 확장하면 좋은 것 (후속 과제)

* **커버리지 임계값 CI 게이트**: 지금은 커버리지를 출력만 하고 강제하진 않음. `judge`
  패키지에 실제 로직이 들어가면 최소 커버리지(%) 미달 시 CI 실패하도록 강화 검토.
* **`testcontainers-go` 기반 통합 테스트**: 2.4절에서 언급한 수동 검증 시나리오 자동화.
* **벤치마크 회귀 감지**: `benchstat` 등으로 PR 전/후 벤치마크 결과를 비교해 성능 저하를
  자동으로 잡는 것 — 트래픽이 실제로 생기고 나서(최적화가 "측정 가능한 문제"가 된 후) 도입.
* **pprof를 프로덕션에서도 안전하게 쓰는 법**: 지금은 로컬 전용으로만 문서화함. 운영
  환경에서 필요해지면 인증/네트워크 격리를 반드시 먼저 설계할 것.