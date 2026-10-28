# [#35] web 인스턴스를 ctf-instances 네트워크로 — 컨테이너 이름 접속, host 포트 제거

> 선행: pwn은 ctf-pwn으로 분리됨. ADR-0017(격리 경계) 2-2 참고합니다.
> 해당 이슈는 **web(HTTP) 인스턴스의 네트워크**를 다룰 예정입니다. pwn(TCP) 경로는 안 건드립니다.
> 프록시(웹 백엔드)가 이 네트워크에 붙는 것, 도메인 분리(2-1), 인스턴스별 네트워크(2-2 2단계)는 별도입니다.

### 배경

지금 web 인스턴스는 기본 bridge에 뜨고 호스트 loopback의 임의 포트에 publish된다.
웹 백엔드 프록시가 그 `127.0.0.1:포트`로 붙는다.

ADR-0017이 이걸 바꾸기로 했다. **web 인스턴스를 `ctf-instances` 전용 네트워크에 두고, host 포트를 아예 안 연다.**
프록시는 같은 네트워크에서 **컨테이너 이름으로** 붙는다.

```
프록시 ──▶ http://{컨테이너 이름}:{컨테이너 포트}
```

이렇게 바꾸면 세 가지가 풀린다(ADR-0017).

* **`127.0.0.1` 불일치가 사라진다.** 지금 agent는 `CTF_INSTANCE_HOST=127.0.0.1`을 돌려주는데, 프록시(Spring)는 컨테이너 안에서 돌아서 그 주소가 자기 자신을 가리킨다. 배포 형태에서 안 맞던 버그다.
* **web은 host 포트를 안 쓴다.** 도커 embedded DNS가 이름을 풀어주니 포트를 노출할 이유가 없다. 대외 개방 포트가 80/443뿐이 된다(ADR-0015 목표를 게이트웨이 없이 달성).
* **인터넷이 막힌다.** `ctf-instances`를 `internal: true`로 만들면 web 컨테이너가 밖으로 못 나간다. reverse shell·외부 공격이 닫힌다.

### 설계

**1. `ctf-instances` 네트워크를 만든다 (internal)**

`ctf-pwn`과 같은 방식이다. 다만 이건 `internal: true`다(포트를 publish 안 하므로 가능하고, 그래야 인터넷이 막힌다).
compose 서비스가 안 쓰므로 compose가 안 만든다 → `docker network create`로 미리 만들고 compose엔 external로 표시한다(runbook).

```
docker network create --internal ctf-instances
```

**2. runtime.Run이 "publish 안 함" 모드를 받는다**

지금 `RunSpec`은 HTTP(zero value)면 loopback 임의 포트에 publish한다. 이걸 바꾼다.

* `RunSpec`에 "host 포트를 열지 여부"를 명시한다(예: `Publish bool`).
* web(HTTP): `Publish=false`, `Network=ctf-instances`. PortBindings를 안 건다.
  이때 `Run`이 돌려주는 포트는 host 포트가 아니라 **컨테이너가 EXPOSE한 포트**다(프록시가 그 포트로 붙으므로).
* pwn(TCP): 지금 그대로. `Publish=true`, 할당 포트, `ctf-pwn`.

**3. handler가 web은 컨테이너 이름을 host로 돌려준다**

web은 참가자에게 보여줄 외부 주소가 아니라, 프록시가 붙을 **내부 이름**을 돌려줘야 한다.

* HTTP 응답 `Host` = 컨테이너 이름(`instanceName`이 만든 `ctf-{code}-{uuid}` 또는 `ctf-{code}`), `Port` = EXPOSE 포트.
* `CTF_INSTANCE_HOST`(127.0.0.1)는 web 경로에서 더는 안 쓴다.

**컨테이너 이름을 그대로 host로 써도 되나?**

ADR-0017은 "이름 = uuid"를 전제로 `http://{uuid}:port`라고 적었다. 그런데 우리 이름은 `ctf-{code}-{uuid}`다.
embedded DNS는 컨테이너 이름을 풀어주므로, **agent가 실제 만든 이름을 그대로 돌려주면** 프록시가 그 이름으로 붙을 수 있다.
이름 규칙이 바뀌면 라우팅이 깨지므로, 프록시는 agent가 준 host를 그대로 써야 한다(스스로 이름을 조립하지 않는다).

**4. Existing / 재사용 경로도 이름·EXPOSE 포트로**

재배달로 이미 떠 있는 web 인스턴스를 돌려줄 때(`Existing`)도 host=이름, port=EXPOSE 포트여야 한다.
지금은 `Running`이 host 포트를 읽어 돌려주는데, publish를 안 하면 그 값이 없다. EXPOSE 포트를 읽어 돌려주도록 바꾼다.

### 작업

* [ ] `runtime/docker.go`: `RunSpec`에 publish 여부, HTTP면 publish 생략 + EXPOSE 포트 반환, `ctf-instances` 부착
* [ ] `runtime/docker.go`: `Running`(Existing용)이 publish 없는 컨테이너의 EXPOSE 포트를 돌려주도록
* [ ] `agentserver/handler.go`: web 응답 host=컨테이너 이름, port=EXPOSE 포트. pwn은 그대로
* [ ] `docker-compose.yml`: `ctf-instances`를 external로 표시
* [ ] `docs/runbook`: `ctf-instances` 생성(`--internal`)과 프록시 연결 절차
* [ ] 테스트: web이 ctf-instances에 붙고 host 포트가 없음, host=이름으로 응답, pwn 회귀 없음

### 완료 조건

* [ ] web 인스턴스가 `ctf-instances`에 붙고 host 포트가 열리지 않는다(`docker inspect`에 PortBindings 없음)
* [ ] CREATE 응답의 host가 컨테이너 이름, port가 EXPOSE 포트다
* [ ] web 컨테이너 안에서 인터넷으로 못 나간다(`internal: true`)
* [ ] 같은 네트워크(프록시 역할)에서 그 이름:포트로 HTTP 응답을 받는다
* [ ] pwn(TCP) 경로는 이 변경 전과 동작이 같다 (host 포트 publish, ctf-pwn)
* [ ] `go build ./...`와 `go test ./agent/...`가 통과한다

### 이번에 하지 않는 것

* **프록시(웹 백엔드)를 ctf-instances에 연결.** `backend-blue`가 이 네트워크에 붙어 이름으로 라우팅하는 건 웹 레포 compose·코드 작업이다. 이 이슈는 agent가 이름:포트를 돌려주는 데까지다.
* **도메인 분리(ADR-0017 2-1).** `inst-{uuid}.nimda-ctf.kr` 별도 도메인·와일드카드 인증서·쿠키 경계는 웹 레포 작업이다.
* **인스턴스별 네트워크(2-2 2단계).** 지금은 `ctf-instances` 하나라 web 인스턴스끼리는 여전히 서로 보인다(ADR-0017이 "현상 유지"로 남겨둔 부분). 동시 인스턴스 수가 정해지면 다시 본다.
* **로컬 개발 흐름 변경.** host 포트가 없어지면 `127.0.0.1:포트` 직접 접속이 안 된다. 로컬에서 프록시 없이 인스턴스를 찌르던 방식(통합테스트 문서)은 따로 손봐야 한다.
* **기존 통합 컨테이너 테스트.** `TestAgentBuildsRunsAndIsReachable`은 127.0.0.1로 붙는 전제라 web 경로 변경과 함께 고쳐야 한다. 이 이슈 작업에 포함한다(위 테스트 항목).
