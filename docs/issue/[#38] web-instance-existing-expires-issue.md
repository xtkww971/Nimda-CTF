# [#38][agent] PER_USER 재사용 응답에 expiresAt 싣기

> 선행: #33 PER_USER reaper(registry에 expiresAt 저장). ADR-0014 참고합니다.
> 해당 이슈는 **이미 떠 있는 PER_USER 인스턴스를 돌려줄 때(`Existing`) 응답의 만료 시각**을 다룰 예정입니다.
> SHARED는 `Join`이 lease 만료 시각을 넣고 있어서 해당 없습니다.

### 배경

웹 백엔드는 READY 결과를 받으면 결과·소유자·사용자 매핑을 **응답의 `expiresAt`까지** 보관한다
(웹 레포 `docs/issue/instance-bugfix-issue.md` B2). 기본 보관 시간(결과 10분)이 인스턴스 수명(1시간)보다 짧아서
10분 뒤부터 참가자가 못 붙던 버그를 이렇게 고쳤다.

그런데 `createPerUser`에서 registry 기록이 있고 컨테이너가 살아 있으면 `Existing`이 응답을 만든다.

```go
res := &protocol.ResponseMessage{
    UUID: uuid, Action: protocol.ActionCreate, Status: protocol.StatusReady,
    InstanceID: instanceID, Host: host, Port: port,
}
```

**`ExpiresAt`이 비어 있다.** 웹은 만료 시각을 모르니 기본값(10분)으로 보관한다.
웹은 결과가 만료되면 같은 uuid로 다시 요청하는데(재접속), 그때마다 이 경로를 타서 **재사용한 인스턴스는 계속 10분짜리**가 된다.

SHARED는 `createShared`가 `Existing` 뒤에 `Join`으로 lease 만료 시각을 넣어서 문제없다.

### 설계

registry `Entry`에 이미 `ExpiresAt`이 있다(`Save`가 저장). `createPerUser`가 읽은 `entry`의 값을 응답에 싣는다.

```go
if res := d.instances.Existing(ctx, req.UUID, entry.InstanceID, req.AccessType); res != nil {
    if !entry.ExpiresAt.IsZero() {
        res.ExpiresAt = entry.ExpiresAt.UTC().Format(time.RFC3339)
    }
    ...
}
```

`Existing`의 시그니처를 바꾸지 않고 dispatcher에서 채운다. 만료 시각을 정하는 건 handler(생성 시)와 registry(기록)이고,
`Existing`은 "떠 있나"만 보는 역할이라 그대로 둔다.

**만료 시각을 연장하지는 않는다.** 재사용은 같은 인스턴스를 다시 알려주는 것이지 수명을 늘리는 게 아니다.
reaper는 처음 정한 시각에 회수한다.

### 작업

* [ ] `agentqueue/dispatcher.go`: PER_USER `Existing` 응답에 `entry.ExpiresAt` 채우기
* [ ] 테스트: 같은 uuid로 두 번 요청 → 두 번째 응답의 `expiresAt`이 첫 번째와 같다

### 완료 조건

* [ ] PER_USER 재사용 응답에 `expiresAt`이 있고 처음 생성 때 값과 같다
* [ ] registry에 만료 시각이 없으면(zero) 지금처럼 비워서 보낸다
* [ ] SHARED 응답은 바뀌지 않는다
* [ ] `go test ./agent/...`가 통과한다

### 이번에 하지 않는 것

* **인스턴스 연장 API.** 참가자가 수명을 늘리는 기능은 protocol에 새 action이 필요하다. 별도로 다룬다.
