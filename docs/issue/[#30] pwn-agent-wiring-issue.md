# [#30] pwn 포트 풀 배선 — runtime·agentqueue·handler·main

> 선행: registry 포트 풀, protocol `AccessType`·provisioner 전달 완료. ADR-0020, ADR-0013 참고합니다.
> 해당 이슈는 **agent의 runtime·agentqueue·handler·main** 배선을 다룰 예정입니다.
> 이 이슈가 끝나면 PWN 인스턴스가 포트 풀에서 포트를 받아 밖으로 열리고, 참가자가 `nc`로 붙을 수 있게 됩니다.
> `ctf-pwn` 네트워크 생성·egress 차단 같은 인프라 설정은 코드가 아니라 별도 작업으로 다룰 예정입니다.

### 배경

#29 에서 포트를 나눠주는 registry API(`AllocatePort`/`ReleasePort`/`InitPortPool`)를 만들었다.
하지만 해당 API를 호출하는 부분을 구현하지 않았기에 PWN 인스턴스는 
provisioner가 `accessType=TCP`를 보내도 WEB과 똑같이 loopback 임의 포트로 뜬다.
밖에서 `nc`로 접속할 수 없다.

이 이슈에서 추가된 API를 이용한 접속 분기를 나누는 것을 목적으로 한다.
TCP면 포트 풀에서 포트를 받아, 그 포트를 호스트에 열고, 참가자에게 주소를 돌려준다.

**누가 포트를 할당하나?**

registry를 쥐고 있는 건 dispatcher다(agentqueue).
handler는 도커 관련 정보만 알고 registry 정보는 알지 못한다.
그래서 포트 할당·반납·기록은 dispatcher가 하고, handler/runtime은 "이 포트를 이 IP·네트워크로 열어라"만 받는다.
지금의 계층 분리(dispatcher=registry·조율, handler/runtime=도커)를 그대로 유지한다.

WEB의 네트워크를 `ctf-instances`로 옮기는 건 ADR-0017의 일이고 이 이슈 밖이다.
WEB은 지금처럼 loopback 임의 포트, 기본 네트워크 그대로 둔다.
이 이슈에서 달라지는 건 TCP 경로뿐이다.

### 설계

**1. runtime.Run에 접속 방식별 옵션을 넣는다**

지금 `Run`은 항상 `127.0.0.1`의 랜덤 포트에 열고 네트워크를 안 지정한다.
TCP면 지정 포트·바인딩 IP·`ctf-pwn` 네트워크로 열어야 한다.

`Run`에 옵션을 받는 형태로 바꾼다. 값은 dispatcher/handler가 채워 넘긴다.

| | HTTP (web) | TCP (pwn) |
| --- | --- | --- |
| 바인딩 IP | `127.0.0.1` | `CTF_PWN_BIND_IP` (기본 `0.0.0.0`) |
| host 포트 | 빈 값(데몬이 고름) | 할당받은 포트 |
| 네트워크 | 지정 안 함(기본 bridge) | `ctf-pwn` |
| `nimda.ctf.port` 라벨 | 없음 | 할당받은 포트 |

`nimda.ctf.port` 라벨을 붙이는 이유는, 재시작 시 `InitPortPool`이 "지금 떠 있는 포트"를 이 라벨로 읽기 때문이다(이슈 #29).

**2. runtime에 UsedPorts를 추가한다**
시작할 때 풀을 다시 계산하려면 "지금 떠 있는 컨테이너가 쓰는 포트"가 필요하다(이슈 #29의 `InitPortPool`).
도커를 보는 일이라 runtime이 한다.
(registry에는 FreePorts가 있고 runtime에는 UsedPorts가 존재하게 된다.)

`UsedPorts(ctx) ([]int, error)`
`nimda.ctf` 라벨이 붙은 컨테이너를 훑어 `nimda.ctf.port` 값을 모은다.

**3. handler.Create가 포트·접속 방식을 runtime에 넘긴다**

handler는 req.AccessType을 보고 Run 옵션을 만든다.
**할당 받은 포트는 dispatcher가 넘겨준다.** handler는 registry를 모르기 때문이다.

* `Create`가 할당 포트를 받도록 바꾼다(0이면 데몬이 고름 = HTTP).
* TCP면 응답 `Host`를 `CTF_PWN_PUBLIC_HOST`로 채운다. WEB의 `CTF_INSTANCE_HOST`와 다르다.
  참가자에게 보여줄 주소이기 때문이다.

**4. dispatcher.createPerUser가 TCP면 포트를 할당한다**

```
TCP면:
  AllocatePort → 실패(ok=false)면 FAILED "지금 생성할 수 있는 인스턴스가 없습니다"
  Create(req, port)
    실패면 ReleasePort로 되돌리고 FAILED
    성공이면 Save(uuid, instanceID, port)
HTTP면: 지금 그대로 (port 0)
```

**컨테이너 재생성 시 옛 포트는 어떻게 하나?**

registry에 기록은 있는데 컨테이너가 죽은 경우, 다시 만들기 전에 기록된 `Entry.Port`를 먼저 반납한다.
안 그러면 그 포트가 풀에서 영영 사라진다.
(이미 떠 있으면 `Existing`이 현재 포트를 그대로 돌려주므로 재할당하지 않는다.)

**5. destroy가 TCP 포트를 반납한다**

PER_USER 컨테이너를 내릴 때, `Entry.Port`가 0이 아니면 `ReleasePort`로 되돌린다.
컨테이너 삭제가 성공한 뒤에 반납한다. 삭제가 실패했는데 포트만 풀로 돌아가면 다음 사람이 그 포트에 붙었다 죽은 컨테이너를 본다.

**6. agent main이 시작할 때 풀을 초기화한다**
redis에서 조회해서 초기화할 예정
main이 재시작하는 경우를 대비
```
env:
  CTF_PWN_PORT_RANGE   기본 "30000-30199"
  CTF_PWN_BIND_IP      기본 "0.0.0.0"
  CTF_PWN_PUBLIC_HOST  (TCP를 쓸 거면 필수)
  CTF_PWN_NETWORK      기본 "ctf-pwn"

시작 순서:
  used = runtime.UsedPorts(ctx)
  registry.InitPortPool(ctx, lo, hi, used)
```

Redis를 기다린 뒤, 요청을 받기 전에 부른다.
그래야 재계산과 할당이 겹치지 않는다(이슈 #29 의 `InitPortPool` 주석).

### 작업

* [ ] `runtime/docker.go`: `Run`이 접속 방식별 옵션(바인딩 IP, host 포트, 네트워크, port 라벨)을 받도록 변경
* [ ] `runtime/docker.go`: `UsedPorts` 추가
* [ ] `agentserver/handler.go`: `Create`가 할당 포트를 받아 Run에 넘기고, TCP면 `Host`를 pwn 공개 주소로 채움
* [ ] `agentqueue/dispatcher.go`: `createPerUser`의 TCP 분기(할당→Create→Save, 실패 시 반납, 재생성 시 옛 포트 반납)
* [ ] `agentqueue/dispatcher.go`: `destroy`가 TCP 포트 반납
* [ ] `agentqueue/dispatcher.go`: `Registry`/`InstanceHandler` 인터페이스에 필요한 메서드 추가
* [ ] `cmd/agent/main.go`: pwn 관련 env, 시작 시 `UsedPorts`+`InitPortPool` 호출, 네트워크·바인딩·공개주소 주입
* [ ] 테스트: TCP 할당→기록→반납 흐름, 풀 소진 시 FAILED, 재생성 시 옛 포트 반납, 반납 시 포트 복귀

### 완료 조건

* [ ] `accessType=TCP` 요청이 오면 `30000-30199` 안의 포트로 컨테이너가 뜨고, 응답 `host:port`가 그 포트를 가리킨다
* [ ] 다른 호스트에서 `nc`로 그 포트에 붙어 `03-pwn-bof`를 풀 수 있다
* [ ] 풀이 비면 FAILED로 끝나고 컨테이너를 만들지 않는다
* [ ] DESTROY하면 컨테이너가 지워지고 포트가 풀로 돌아온다
* [ ] agent를 죽였다 다시 띄워도, 떠 있는 컨테이너가 쓰는 포트는 다시 할당되지 않는다
* [ ] WEB 요청은 이 변경 전과 동작이 같다(여전히 loopback 임의 포트, 기본 네트워크)
* [ ] `go build ./...`와 `go test ./agent/...`가 통과한다

### 이번에 하지 않는 것

* **`ctf-pwn` 네트워크 생성과 egress 차단.** `docker network create`, `DOCKER-USER` iptables, 보안 그룹 `30000-30199`. 코드가 아니라 인프라 설정이라 별도 작업이다. 이 이슈는 네트워크 이름만 지정하고, 네트워크가 미리 있다고 가정한다.
* **WEB의 네트워크 이전.** `ctf-instances`, 컨테이너 이름 라우팅은 ADR-0017의 일이다.
* **reaper의 TTL 만료 포트 반납.** reaper가 아직 없다(ADR-0014). 이 이슈는 DESTROY와 시작 시 재계산으로만 반납한다.
* **gVisor(runsc) 확인.** 포트가 열린 뒤 `runc`/`runsc` 양쪽에서 exploit이 되는지는 테스트 문제로 따로 확인한다.
