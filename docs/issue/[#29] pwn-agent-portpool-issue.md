# [#29] pwn 포트 풀 — 할당·반납·재계산 (agent/registry)

> 선행: protocol `AccessType`(완료), provisioner accessType 전달(완료). ADR-0020, ADR-0013 참고.
> 해당 이슈는 **registry 모듈의 저장소 계층만** 다룰 예정입니다.
> 실제로 포트를 밖에 여는 일(runtime.Run, CREATE/DESTROY 분기)은 추후 다음 이슈로 다룰 예정입니다.

### 배경

pwn은 `nc host port`로 컨테이너에 바로 붙는다(ADR-0020).
그러려면 인스턴스마다 포트를 하나 받아 밖에 열어야 한다.
포트는 `30000-30199` 같은 정해둔 풀에서만 나눠준다(ADR-0013).

지금 WEB의 접속 포트 설정은 Docker 엔진의 loopback 임의 포트로 되어 있다.
pwn은 WEB과 프로토콜이 다르고 포트 풀을 따로 두기 때문에 WEB과 분리해야 한다.
또한 이렇게 해야 배포 시 EC2 방화벽 설정과 일치시킬 수 있다.

이 이슈에서는 "포트를 나눠주고 돌려받는" 저장소 API만 만든다.
실제 API 호출 파이프라인은 다음 이슈에서 다룰 예정입니다.

**컨테이너 풀 상태를 redis registry에 저장하는 이유**
agent가 재시작될 때 메모리가 초기화되기 때문이다.
그런데 컨테이너는 그대로 떠 있어서, 이미 쓰는 포트를 다시 나눠주게 된다(ADR-0013).
그래서 풀은 조율자와 공유하는 ctf-redis에 둔다.

### 설계

**1. 풀 상태**

```
ctf:ports:free   (Set)   아직 안 나간 포트 번호 문자열
```

할당은 `SPOP` 하나로 끝난다. redis는 원자적이라 lock이 필요 없다.
랜덤으로 꺼내서 포트 번호로 다음 사람 인스턴스를 짐작하기도 어렵다.

**2. registry에 붙일 메서드**

* `AllocatePort(ctx) (port int, ok bool, err error)`
  `SPOP ctf:ports:free`. 풀이 비면 `ok=false`. 부르는 쪽은 이때 FAILED로 돌려준다.
* `ReleasePort(ctx, port) error`
  `SADD`로 되돌린다. 같은 포트를 두 번 넣어도 Set이라 중복이 안 생긴다.
* `InitPortPool(ctx, lo, hi, usedPorts) error`
  풀을 다시 계산한다. `free = {lo..hi} - usedPorts`.
  `usedPorts`는 지금 떠 있는 `nimda.ctf` 컨테이너의 `nimda.ctf.port` 라벨값이다.
  이 목록을 모으는 건 도커를 보는 일이라 runtime이 하고(이슈 B), registry는 숫자 집합만 받는다.

**docker와 redis의 정보 계산을 시작할 때 하는 이유?**

반납을 못 하고 죽는 경우가 반드시 생긴다(ADR-0013).
그때 그 포트는 풀에서 사라진 채로 남는다.
도커에 실제로 떠 있는 컨테이너를 기준으로 다시 계산하면, redis 값이 어떻게 틀어져 있든 맞춰진다.
agent가 시작할 때 한 번 부른다. 나중에 reaper가 생기면 주기마다 같은 계산을 붙인다(ADR-0014, 이 이슈 밖).

**3. `uuid:` hash에 port 필드**

TCP 인스턴스는 받은 포트를 기록해야 반납할 때 어느 포트였는지 안다.

* `Entry`에 `Port int` 추가.
* `Save`가 port를 같이 저장하도록 바꾼다. web(HTTP)은 포트를 안 받으므로 0이고, 0이면 저장하지 않는다.

`Save` 시그니처가 바뀌면 부르는 쪽(dispatcher)도 같이 바뀌는데, 그 호출부 수정은 다음 이슈에서 한다.
이 이슈에서는 registry만 바꾸고, 기존 호출부가 깨지지 않게 port를 받는 형태를 정한다.

**port를 Save 인자로 받을까, 별도 메서드로 둘까?**

`Save(ctx, uuid, instanceID, port)`로 인자를 하나 늘린다.
port가 0이면 지금과 똑같이 동작한다(HTTP). 메서드를 둘로 나누면 "PER_USER인데 어느 걸 부르지"가 생긴다.
한 메서드가 port 0을 "포트 없음"으로 다루는 쪽이 단순하다.

### 작업

* [ ] `registry.go`: `ctf:ports:free` 키 상수, `fieldPort` hash 필드
* [ ] `registry.go`: `AllocatePort`, `ReleasePort`, `InitPortPool`
* [ ] `registry.go`: `Entry`에 `Port int` 추가, `Entry()` 파싱에 port 추가
* [ ] `registry.go`: `Save`가 port를 받아 저장 (0이면 생략)
* [ ] `registry_test.go`: 아래 완료 조건을 miniredis로 테스트

### 완료 조건

* [ ] `InitPortPool(30000, 30002, {30001})` 뒤 `free`에 30000, 30002만 있다
* [ ] `AllocatePort`를 풀 크기만큼 부르면 서로 다른 포트가 나오고, 그 다음엔 `ok=false`다
* [ ] `ReleasePort`로 돌려준 포트가 다시 `AllocatePort`로 나온다
* [ ] 같은 포트를 두 번 `ReleasePort`해도 풀에 하나만 생긴다
* [ ] `Save`에 port를 주면 `Entry().Port`로 되읽힌다. port 0이면 hash에 `port` 필드가 없다
* [ ] `go build ./...`와 `go test ./agent/...`가 통과한다 (기존 테스트 회귀 없음)

### 이번에 하지 않는 것

* **포트를 실제로 여는 일.** `runtime.Run`에 바인딩 IP·지정 포트·`ctf-pwn` 네트워크를 넣는 것. 이슈 B.
* **CREATE/DESTROY의 TCP 분기.** 언제 `AllocatePort`/`ReleasePort`를 부르는가. 이슈 B.
* **`nimda.ctf.port` 라벨을 붙이는 것.** 컨테이너를 띄우는 이슈 B에서 붙인다. 이 이슈의 `InitPortPool`은 그 라벨을 읽기만 한다(값은 호출부가 모아 넘긴다).
* **reaper의 포트 반납·주기적 재계산.** reaper 자체가 아직 없다(ADR-0014). TTL 만료 시 반납은 그때 붙인다.
* **풀 범위를 정하는 설정(env).** 범위 기본값·주입은 이슈 B에서 agent main이 `InitPortPool`을 부를 때 정한다.
