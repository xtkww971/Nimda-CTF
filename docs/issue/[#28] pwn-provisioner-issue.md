# [#28][provisioner] challengeCategory로 accessType를 정해 agent에 넘긴다

> 선행: protocol에 `AccessType` 추가(완료). ADR-0020, `pwn-peruser-test-report.md` 참고.
> 이 이슈는 **provisioner 모듈만** 다룬다. agent의 포트 풀·네트워크 분기는 별도 이슈다.

### 배경

protocol에 `AccessType`(HTTP/TCP)이 생겼고 CREATE 필수 필드가 됐다.
그런데 provisioner는 아직 이 값을 안 싣는다.
그래서 지금 상태로 배포하면 agent 파싱에서 `accessType`이 비어 CREATE가 전부 FAILED가 된다.
protocol과 이 작업은 같이 배포해야 한다.

`accessType`은 웹이 보내는 값이 아니다.
웹은 지금처럼 `challengeCategory`(WEB, PWN ...)만 보낸다.
agent는 문제 유형을 몰라야 하므로(ADR-0020), 카테고리를 접속 방식으로 바꾸는 일은 provisioner가 맡는다.

**매핑을 어디에 둘까?**

카테고리를 해석하는 곳은 이미 `handler/task.go`다.
`instanceCategories`가 "어떤 카테고리가 인스턴스를 띄우는가"를 들고 있고, `classify`가 그걸 본다.
"어떤 카테고리가 어떻게 붙는가"도 같은 지식이라 여기 두는 게 맞다.
`dispatch.go`는 지금 카테고리를 보지 않는다. 그대로 두고, task.go가 정한 값을 받아 `RequestMessage`에 옮기기만 한다.

### 설계

**1. 카테고리 → 접속 방식 테이블을 하나로 둔다**

`instanceCategories`를 `map[string]bool`에서 `map[string]protocol.AccessType`로 바꾼다.
"인스턴스를 띄우는 카테고리"와 "그 카테고리가 어떻게 붙는가"를 한 테이블에서 본다.

```go
// handler/task.go
var instanceCategories = map[string]protocol.AccessType{
    "WEB": protocol.AccessHTTP,
    "PWN": protocol.AccessTCP,
}
```

`classify`는 지금처럼 "이 맵에 있으면 CREATE_INSTANCE"로 판단한다(값이 아니라 존재 여부만 본다).

**2. `CreateInstanceMessage`에 `AccessType`를 싣는다**

`newTaskMessage`의 CREATE 분기에서 테이블 값을 넣는다.
이 분기로 오는 카테고리는 이미 맵에 있는 값(WEB/PWN)이라, 매핑이 비는 경우는 없다.

```go
return &CreateInstanceMessage{
    ...
    AccessType: instanceCategories[strings.ToUpper(strings.TrimSpace(msg.ChallengeCategory))],
}
```

**3. `dispatch.go`가 `RequestMessage`에 그대로 옮긴다**

```go
err = h.agent.Dispatch(ctx, protocol.RequestMessage{
    ...
    IsolationType: protocol.IsolationType(msg.IsolationType),
    AccessType:    msg.AccessType, // ← 추가
    ...
})
```

**4. PWN + SHARED는 provisioner에서 먼저 막는다**

protocol 파서도 TCP + SHARED를 reject하지만, 거기까지 가면 agent 로그에만 남는다.
provisioner에서 먼저 걸러 FAILED 사유를 웹에 돌려주면 "왜 안 뜨는지"가 바로 보인다.
pwn은 셸을 따는 게 정답이라 SHARED로 뜨면 한 명이 모두의 문제를 망친다.

`CreateInstanceMessage`를 처리하기 전에 검사한다.
AccessType이 TCP인데 IsolationType이 SHARED면 FAILED로 끝낸다.

### 작업

* [ ] `handler/task.go`: `instanceCategories`를 `map[string]protocol.AccessType`로 변경 (`WEB→HTTP`, `PWN→TCP`)
* [ ] `handler/task.go`: `CreateInstanceMessage`에 `AccessType protocol.AccessType` 필드 추가
* [ ] `handler/task.go`: `newTaskMessage`가 테이블 값을 `CreateInstanceMessage.AccessType`에 채움
* [ ] `handler/dispatch.go`: `Handler.Handle`이 `RequestMessage`에 `AccessType`를 옮김
* [ ] `handler/dispatch.go`: TCP + SHARED면 빌드 전에 FAILED 반환
* [ ] 테스트: 매핑과 TCP+SHARED 거부를 추가

### 완료 조건

* [ ] `challengeCategory=WEB` 요청이 `accessType=HTTP`로 agent에 전달된다
* [ ] `challengeCategory=PWN` 요청이 `accessType=TCP`로 agent에 전달된다
* [ ] `challengeCategory=PWN` + `isolationType=SHARED`는 FAILED로 끝나고 agent까지 가지 않는다
* [ ] WEB 요청은 이 변경 전과 동작이 같다 (회귀 없음)
* [ ] `GET_PRESIGNED_URL`(정적 문제) 경로는 영향받지 않는다
* [ ] `go build ./...`와 `go test ./provisioner/...`가 통과한다

### 이번에 하지 않는 것

* **agent의 포트 풀·네트워크·바인딩 처리.** `accessType=TCP`를 받아 실제로 포트를 밖에 여는 일. 별도 이슈.
  이 작업 뒤에도 PWN은 빌드·실행까지만 되고, 포트는 아직 loopback이라 밖에서 못 붙는다.
* **웹 메시지 변경.** 웹은 계속 `challengeCategory`만 보낸다. `queue.Request`에 필드를 더하지 않는다.
* **protocol 변경.** 이미 끝났다.
