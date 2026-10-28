# [#36][provisioner] 인스턴스 조회 내부 API — 웹이 매 요청 살아 있는지 묻는 경로 (ADR-0017 2-3)

> 선행: #35, ADR-0012(provisioner가 ctf-internal·nimda-network 양쪽에 붙음), ADR-0017 2-3 참고합니다.
> 해당 이슈는 **provisioner가 웹에 "이 uuid가 살아 있고 어디 있는지"를 답하는 내부 경로**를 다룰 예정입니다.
> 웹 쪽(프록시가 캐시 대신 이 경로를 쓰는 것)은 웹 레포 `docs/issue/instance-live-lookup-issue.md`에서 다룹니다.

### 배경

지금 웹 프록시는 결과 스트림으로 한 번 받은 host/port를 Redis(`ctf:result:{uuid}`)에 캐시해 두고 매 요청 그걸 쓴다.
컨테이너가 reaper에 회수되거나 죽어도 웹은 모른다. 캐시가 남아 있는 동안 502가 나고, 참가자는 "READY인데 안 열린다"를 본다.

ADR-0017 2-3은 이 조회를 **내부망 안에서만** 하기로 정했다.

* 웹이 provisioner를 통해 묻는다. provisioner는 이미 `ctf-internal`과 `nimda-network` 양쪽에 있다.
* host 포트도 nginx 경로도 없다.
* **uuid로 자기를 지키지 않는다.** uuid는 DNS·SNI로 평문으로 다니는 값이라 비밀이 아니다. 닿을 수 있는 범위 자체를 막는다.

**먼저 — 이게 지금 필요한 이슈인가**

#35 이후 응답 host는 "컨테이너 이름", port는 "EXPOSE 포트"라 **재생성해도 안 바뀐다.**
그래서 "캐시가 낡아 죽은 주소를 준다"는 원래 동기가 약해졌다. 캐시가 틀리는 게 아니라, 회수된 인스턴스를 웹이 모를 뿐이다.
즉 이 이슈의 값은 "주소 갱신"이 아니라 **"회수됐으면 502 대신 깔끔한 404를 준다"** 로 좁혀졌다.
그 값이 매 요청 왕복을 들일 만한지부터 판단하고, 아니면 보류한다.

### 설계

**원칙 못: provisioner는 도커를 보지 않는다 (ADR-0009)**

"살아 있나"를 도커에 직접 물으면(`runtime.Running`) provisioner가 컨테이너를 만지는 게 되어 ADR-0009를 깬다.
provisioner는 도커 소켓을 안 본다. 그래서 이 조회는 **ctf-redis의 registry만 읽어서** 답한다.
reaper(#33/#34)가 회수하면서 registry 기록을 지우므로, **"registry에 기록이 있고 만료 전"** 을 살아 있음으로 본다(최대 reaper 주기만큼 늦을 수 있다). 진짜 liveness가 꼭 필요하면 그건 agent에 묻는 별도 경로로 다룬다.

**무엇을 답하나**

```
uuid → { alive: bool, host, port, expiresAt }   # 전부 ctf-redis registry에서 나온다
```

`alive` = `uuid:` 기록이 있고 `expiresAt`이 지나지 않음. provisioner는 ctf-redis만 읽는다(ADR-0012상 가능).

**어떤 방식으로 — 이 이슈에서 정하지 않는다**

웹↔provisioner를 잇는 방식(HTTP 엔드포인트 / Redis 요청·응답 스트림)은 **되돌리기 비용이 큰 계약**이라 구현 이슈에서 정하지 않는다.
특히 "provisioner에 HTTP 서버를 새로 둔다"는 부품을 늘리는 결정이라(ADR-0009·0015), 득실을 ADR에서 먼저 따진다.
**ADR-0017 2-3(또는 새 ADR)에서 방식을 확정한 뒤** 이 이슈가 구현한다.

| 후보 | 장점 | 단점 |
|---|---|---|
| HTTP (nimda-network에만 listen) | 웹에서 부르기 쉽다 | provisioner에 HTTP 서버가 생긴다(지금은 pprof뿐) — 부품·장애점 추가 |
| Redis 요청·응답 스트림 | 지금 구조와 같다, 부품 안 늠 | 매 요청 왕복이 무겁다, 프록시 지연 |
| ~~registry를 웹이 직접 읽기~~ | — | 웹이 ctf-redis(ctf-internal)에 붙어야 함 → ADR-0012·2-3 경계가 깨짐. **후보 아님** |

**접근 범위**

* 웹(nimda-network)에서만 닿는다. host 포트 publish 안 함.
* `ctf-instances`(문제 컨테이너 망)에서는 닿으면 안 된다. provisioner를 그 망에 붙이지 않는다.

### 선행 결정 (이 이슈 착수 전)

* [ ] 이 조회가 지금 필요한가, 아니면 보류인가 (위 "먼저" 참고)
* [ ] 필요하면 방식(HTTP vs 스트림)을 ADR-0017 2-3에 확정

### 작업 (방식 확정 후)

* [ ] provisioner: 조회 응답을 **ctf-redis registry만 읽어** 만든다 (도커 접근 없음)
* [ ] 확정된 방식으로 웹↔provisioner 경로 구현
* [ ] 테스트: 살아 있는 uuid / 회수된(기록 없는) uuid / 없는 uuid, ctf-instances에서 접근 불가

### 완료 조건

* [ ] 웹 backend 컨테이너에서 조회하면 살아 있는 인스턴스의 host/port/expiresAt을 받는다
* [ ] reaper가 회수한 인스턴스는 `alive:false`다 (registry에서 사라졌으므로)
* [ ] provisioner가 도커 소켓을 보지 않는다 (registry만 읽는다)
* [ ] 호스트·인터넷·ctf-instances에서는 이 경로에 닿지 않는다

### 이번에 하지 않는 것

* **방식(HTTP/스트림) 결정.** 계약이라 ADR에서 먼저 정한다. 이 이슈는 그 뒤 구현만.
* **agent에 직접 묻는 liveness.** registry 기준으로 부족하다고 판명되면 그때 별도로.
* **인스턴스 연장·종료.** 조회만 다룬다.
* **pwn.** pwn은 프록시를 안 거치므로 이 경로가 필요 없다.
