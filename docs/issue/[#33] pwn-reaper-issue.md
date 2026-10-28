# [#33] TTL 집행 — 만료 인스턴스 회수 reaper (PER_USER)

> 선행: agent 배선·포트 풀·하드닝 완료. ADR-0014(선언 아닌 집행), ADR-0013(포트 반납) 참고합니다.
> 해당 이슈는 **PER_USER 인스턴스의 만료 집행**을 다룰 예정입니다.
> SHARED의 idle grace 회수는 ADR-0019·0014가 "나중에"로 미뤄둔 조각이라 별도 이슈로 다룰 예정입니다.

### 배경

지금 인스턴스에 만료 시각(`expiresAt`)을 계산해 참가자에게 보낸다.
**그런데 그 시각이 되어도 아무 일도 안 일어난다.** 만료를 읽어 내리는 reaper가 없다(ADR-0014).

그래서 생기는 구멍:

* **PER_USER 인스턴스가 영원히 뜬다.** 상시 워게임이라 누수가 쌓이면 몇 주 뒤 호스트가 멈춘다.
* **포트가 안 돌아온다.** pwn 포트는 DESTROY와 agent 재시작으로만 반납된다(이슈 #30). 참가자가 종료를 안 누르고 나가면 그 포트는 만료돼도 풀에 안 돌아온다.

**더 근본적인 전제가 깨져 있다. PER_USER는 `expiresAt`을 저장조차 안 한다.**
`registry.Save`는 isolation·instanceId·port만 쓴다. 만료 시각은 응답 메시지로 나갈 뿐 registry에 없다.
reaper가 만료를 보려면 먼저 이걸 registry에 적어야 한다.

### 설계

ADR-0014의 원칙 그대로다. **만료는 선언이 아니라 집행이다.** 주기적으로 훑어서 내린다.

**1. 만료 시각을 registry에 저장한다**

handler가 이미 PER_USER `expiresAt`(지금 `perUserTTL=1h`)을 계산한다.
그 값을 dispatcher가 `Save`에 같이 넘겨 registry에 기록한다.

* `Save`에 `expiresAt time.Time` 인자를 더한다. zero면 안 쓴다.
* `Entry`는 이미 `ExpiresAt`을 파싱한다(읽는 쪽은 그대로).

**2. 컨테이너 라벨에도 만료 시각을 박는다**

registry 기록과 실제 컨테이너가 어긋나는 경우가 반드시 생긴다 — 기록 전에 죽거나, 기록을 지운 뒤 삭제에 실패하거나(ADR-0014).
그래서 만료 시각을 도커 라벨 `nimda.ctf.expires`에도 박아, registry에 없는 컨테이너도 라벨로 회수한다(orphan).

* handler가 PER_USER 컨테이너에 `nimda.ctf.expires` 라벨을 붙인다.

**3. reaper를 둔다 — 주기적으로 훑는다**

`agent/internal/reaper`를 새로 만든다. **30초마다** 돈다(ADR-0014).
컨테이너를 만질 수 있는 건 agent뿐이라 여기 둔다(ADR-0009).

```
매 주기:
  ① registry 훑기 (정상 경로)
     각 uuid 기록 중 Isolation==PER_USER 이고 expiresAt이 지난 것
       → 컨테이너 삭제, uuid 기록 삭제, port 있으면 풀에 반납
  ② 도커 훑기 (2차 안전장치, orphan)
     nimda.ctf.expires 라벨이 붙었는데 registry에 uuid 기록이 없는 컨테이너
       → 라벨의 만료 시각이 지났으면 삭제
```

**왜 redis 키 만료 이벤트를 안 쓰나?**

ADR-0014에서 정한 그대로다. Pub/Sub은 fire-and-forget이라 agent 재배포 중 이벤트가 사라진다.
놓친 이벤트 하나가 영원히 뜬 컨테이너다. 주기적 조회는 놓쳐도 다음 주기에 다시 본다.

**순서 — reaper와 CREATE가 같은 포트를 두고 겹치지 않나?**

PER_USER는 uuid가 곧 주인이라 겹치지 않는다. reaper가 지우는 건 만료된 기록이고, CREATE는 새 uuid다.
(SHARED는 reaper와 CREATE가 같은 컨테이너를 두고 경쟁해서 lock이 필요한데, 그건 SHARED 이슈에서 다룬다.)

**4. main에 배선한다**

```
env:
  CTF_REAP_INTERVAL   기본 30s

go reaper.New(rt, reg, log, interval).Run(ctx)
```

monitor와 같은 방식으로 goroutine 하나를 띄운다. 종료 ctx로 같이 내린다.

### 작업

* [ ] `registry.go`: `Save`에 `expiresAt` 저장 추가, 파싱은 이미 있음
* [ ] `agentserver/handler.go`: PER_USER 컨테이너에 `nimda.ctf.expires` 라벨, `Create`가 만료 시각을 넘김
* [ ] `agentqueue/dispatcher.go`: `Save` 호출에 expiresAt 전달 (인터페이스·fake 포함)
* [ ] `runtime/docker.go`: 만료된 컨테이너를 찾는 데 쓸 라벨 상수(`nimda.ctf.expires`), 필요한 조회
* [ ] `agent/internal/reaper`: 주기 goroutine, registry 경로 + orphan 경로, 포트 반납
* [ ] `cmd/agent/main.go`: `CTF_REAP_INTERVAL`, reaper 기동
* [ ] 테스트: 만료된 것 회수·포트 반납, 안 만료된 것 유지, orphan 회수, 삭제 실패 시 다음 주기 재시도

### 완료 조건

* [ ] 만료 시각이 지난 PER_USER 컨테이너가 다음 주기(최대 30초 내)에 삭제된다
* [ ] 삭제될 때 그 포트가 `ctf:ports:free`로 돌아온다
* [ ] 아직 안 만료된 인스턴스는 건드리지 않는다
* [ ] registry 기록이 없어도 `nimda.ctf.expires`가 지난 컨테이너는 회수된다(orphan)
* [ ] 삭제가 실패해도 다음 주기에 다시 시도한다(크래시하지 않는다)
* [ ] `expiresAt`이 registry에 저장되고 `Entry().ExpiresAt`로 읽힌다
* [ ] `go build ./...`와 `go test ./agent/...`가 통과한다

### 이번에 하지 않는 것

* **SHARED idle grace 회수.** lease가 0개가 되고 grace가 지나면 공유 컨테이너를 내리는 것(ADR-0019 ⑤). lock·grace 설정이 얽혀 별도 이슈다. 이 reaper는 `Isolation==PER_USER`만 본다.
* **TTL을 요청 메시지로 받기.** 지금은 `perUserTTL=1h` 하드코딩을 그대로 쓴다. 문제별 TTL은 protocol·웹이 같이 바뀌는 계약 변경이라 별도다(ADR-0014).
* **연장(renew).** 만료 시각을 미루는 기능. ADR-0014가 자리만 잡아둔 상태 그대로다.
* **보조 기록 수명(웹 캐시·소유자 매핑) 정렬.** 웹 쪽 TTL 유도는 웹 레포 작업이다.
