# [#26][protocol] pwn 접속 방식을 담는 AccessType 추가

> 작성 전에 읽을 것: ADR-0020, `docs/features/PwnInstance.md`.
> 이 이슈는 **`protocol` 모듈의 계약 정의만** 다룬다.
> provisioner의 `challengeCategory → accessType` 매핑과 agent의 포트 풀·네트워크는 각각 별도 이슈다.

### 배경

pwn 문제를 포트 풀 방식으로 내기로 했다(ADR-0020).
web은 포트를 밖에 안 열고 웹 프록시로 붙지만, pwn은 `nc host port`로 컨테이너에 바로 붙는다.
raw TCP라 Host 헤더가 없어서 프록시에 못 태우기 때문이다.

그래서 agent는 컨테이너를 띄울 때 "이 인스턴스가 포트를 밖에 여는가"를 알아야 한다.
포트를 여느냐 마냐에 따라 붙이는 네트워크(`ctf-instances` vs `ctf-pwn`)와 바인딩 IP가 달라진다.

이 값을 agent에게 어떻게 넘길지가 이번 계약의 핵심이다.

**왜 `challengeCategory`를 그대로 agent에 안 넘기나?**

카테고리는 계속 늘어난다(WEB, PWN, CRYPTO, MISC ...).
agent가 카테고리를 알면 카테고리가 생길 때마다 agent도 고쳐야 한다.
agent가 실제로 알아야 하는 건 "포트를 밖에 여는가" 하나뿐이다.
그래서 카테고리 대신 **접속 방식(AccessType)** 을 넘긴다.
카테고리 → 접속 방식 변환은 provisioner가 맡는다(별도 이슈).

이 구조는 isolation type과 같다.
agent는 "문제 유형"이 아니라 "어떻게 격리하고 어떻게 붙는가"만 받는다.

### 설계

`protocol`에 `AccessType`을 추가한다. `IsolationType`과 같은 모양으로 만든다.

```go
// protocol/message.go
type AccessType string

const (
    AccessHTTP AccessType = "HTTP" // 웹 프록시를 거친다. 포트를 밖에 열지 않는다
    AccessTCP  AccessType = "TCP"  // 포트 풀에서 포트를 받아 밖에 연다 (pwn)
)

// 모르는 값을 TCP로 흘려보내면 의도치 않게 포트가 밖에 열린다.
func (t AccessType) Valid() bool {
    return t == AccessHTTP || t == AccessTCP
}
```

`RequestMessage`에 필드를 하나 추가한다.

```go
type RequestMessage struct {
    UUID   string `json:"uuid"`
    Action string `json:"action"`

    ChallengeCode string        `json:"challengeCode,omitempty"`
    IsolationType IsolationType `json:"isolationType,omitempty"`
    AccessType    AccessType    `json:"accessType,omitempty"` // ← 추가
    FlagType      string        `json:"flagType,omitempty"`
    Flag          string        `json:"flag,omitempty"`

    BuildContextPath string `json:"buildContextPath,omitempty"`
}
```

stream 직렬화(`ToStreamValues`)와 파싱(`ParseRequestMessage`)에 `accessType`을 넣는다.
파싱의 `ActionCreate` 검증에 두 가지를 추가한다.

* **빈 값과 모르는 값은 reject한다.** `IsolationType`과 똑같이 `Valid()`로 막는다.
* **`TCP` + `SHARED`는 reject한다.** pwn이 공유 컨테이너로 뜨면, 한 명이 셸을 따서 모두의 문제를 망친다.
  isolation type과 access type을 교차로 막는 자리가 여기다.

**string 타입으로 두는 이유 (bool 안 쓰는 이유)**

`shared: true`처럼 bool로 두면 값이 없을 때 자동으로 false가 된다.
필드가 빠진 메시지가 와도 에러 없이 한쪽으로 처리된다.
잘못된 값을 막으려고 타입을 만드는 건데 bool로는 그걸 못 한다.
ADR-0019에서 `IsolationType`을 string으로 둔 것과 같은 이유다.

**결정이 필요한 것 — `accessType`을 CREATE 필수로 둘까?**

추천: **필수로 둔다.** `challengeCode`, `isolationType`처럼 CREATE에 반드시 있어야 하고, 빈 값이면 reject한다.

근거는 "모르면 안전한 쪽"이 명확히 없기 때문이다.
빈 값을 HTTP로 치면 pwn 문제가 프록시로 가서 아예 안 붙고, TCP로 치면 web 문제 포트가 밖에 열린다.
둘 다 조용한 사고라, 빠진 걸 에러로 드러내는 게 낫다.

다만 이렇게 하면 **provisioner가 `accessType`을 보내기 전까지는 CREATE가 전부 실패한다.**
그래서 이 이슈(protocol)와 provisioner 매핑 이슈는 **같은 PR 묶음으로 배포**해야 한다.
protocol만 먼저 배포하면 web 인스턴스 생성이 멈춘다.

### 작업

* [ ] `protocol/message.go`: `AccessType` 타입, `AccessHTTP`/`AccessTCP` 상수, `Valid()` 메서드
* [ ] `protocol/message.go`: `RequestMessage`에 `AccessType` 필드 추가
* [ ] `protocol/stream.go`: `ToStreamValues`에 `accessType` 직렬화
* [ ] `protocol/stream.go`: `ParseRequestMessage`에 `accessType` 파싱
* [ ] `protocol/stream.go`: `ActionCreate` 검증에 빈 값·모르는 값 reject 추가
* [ ] `protocol/stream.go`: `TCP` + `SHARED` 조합 reject 추가
* [ ] `protocol/stream_test.go`: 아래 완료 조건을 테스트로 추가

### 완료 조건

* [ ] `accessType: HTTP`, `accessType: TCP`가 round-trip(직렬화 → 파싱)으로 보존된다
* [ ] `accessType`이 빈 CREATE 메시지는 파싱에서 에러가 난다
* [ ] `accessType`이 `HTTP`/`TCP` 외의 값이면 파싱에서 에러가 난다
* [ ] `accessType: TCP` + `isolationType: SHARED`는 파싱에서 에러가 난다
* [ ] `accessType: HTTP` + `isolationType: SHARED`는 통과한다 (무상태 web 공유 문제)
* [ ] `go build ./...`와 `go test ./protocol/...`이 통과한다

### 이번에 하지 않는 것

* **provisioner의 `challengeCategory → accessType` 매핑.** `PWN → TCP`, `WEB → HTTP`. 별도 이슈.
* **agent의 포트 풀·네트워크·바인딩 IP 처리.** `AccessType`을 보고 실제로 포트를 여는 일. 별도 이슈.
* **`ResponseMessage` 변경.** TCP일 때 `host`/`port`가 "참가자에게 보여줄 값"이 된다는 의미만 달라지고, 필드는 그대로다.
* **웹 메시지 변경.** 웹은 지금처럼 `challengeCategory`만 보낸다. accessType은 provisioner가 정한다.
