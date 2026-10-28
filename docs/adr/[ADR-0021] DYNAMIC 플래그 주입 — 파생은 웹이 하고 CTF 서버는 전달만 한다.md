* **날짜:** 2026-10-28
* **작성자:** 이도현
* **상태:** 보류 (회의 대기). 플래그 주입 방식은 다음 주 회의에서 TTL과 함께 정한다. 구현은 그 뒤에 한다.

## 1. Context (배경)

CTF 문제의 플래그에는 두 종류가 있다.

* **STATIC.** 모두에게 같은 플래그다. 문제 이미지에서 직접 설정
* **DYNAMIC.** 참가자마다 플래그가 다르다. 한 명이 자기 플래그를 남에게 흘려도 그 값은 남의 계정에선 안 통한다. 답 공유(컨닝)를 막는다.

지금 Provisioner는 Flag 값에 따른 로직 분기가 되어 있다.

* `protocol.RequestMessage`에 `Flag` 필드가 있다(`message.go`).
* agent는 `req.Flag`가 차 있으면 컨테이너에 `FLAG` 환경변수로 넣는다(`agentserver/handler.go`).
* SHARED + DYNAMIC 조합은 protocol 파서가 reject한다(`stream.go`). SHARED는 여럿이 한 컨테이너를 같이 쓰는데 거기에 참가자별 플래그를 심을 수 없기 때문이다(ADR-0019·0020).

아직 웹에서 정답 처리 로직이 구현되어 있지 않아
검증이 되지 않는 상태이다.

* provisioner가 agent로 넘길 때 `Flag`를 비워 보낸다(`dispatch.go`).
* 애초에 웹이 provisioner로 보내는 큐 메시지(`queue.Request`)에 플래그 값을 담을 자리가 없다. `flagType`까지만 온다.

그래서 지금 DYNAMIC 문제는 `FLAG` env가 안 들어가고, STATIC만 동작한다.

결정할 것은 하나다.
**DYNAMIC 플래그 값을 누가 만들어서 provisioner에 주는가?**

고려 사항이 셋 있다.

* 플래그 제출·정답 검증·스코어보드는 웹 레포의 몫이다(ADR-0016·0018). CTF 서버는 요청한 사람이 누구인지 모르고 uuid만 안다.
* 플래그는 `nimda-redis`(웹) → provisioner → `ctf-redis`(내부) → agent 경로로 흐른다. 두 통은 분리돼 있고 provisioner만 둘에 걸친다(ADR-0012).
* 문제 컨테이너는 뚫으라고 만든 적대적 환경이다(ADR-0017). 컨테이너 안에서 자기 `FLAG` env는 당연히 보인다.

## 2. Decision (결정)

> 플래그 주입 방식은 다음 주 회의에서 TTL과 함께 정한다.
> 아래는 회의 전 제안이다. 확정 전까지 구현하지 않는다.

**웹이 DYNAMIC 플래그를 파생해서 요청 메시지에 실어 보낸다. CTF 서버는 받은 값을 그대로 agent까지 전달만 한다.**

```
웹(파생) ──flag──▶ provisioner(그대로 복사) ──flag──▶ agent(FLAG env 주입) ──▶ 컨테이너
```

**왜 웹이 파생하나?**

정답을 아는 쪽이 검증하는 쪽이어야 하기 때문이다.
검증은 웹이 한다. 그러니 웹은 어차피 플래그 정답을 알아야 한다.

CTF 서버가 파생하면 두 가지 중 하나가 생긴다.
웹이 그 값을 되받아 저장하거나(결과 경로에 flag를 얹어 왕복), 양쪽이 같은 secret으로 같은 식으로 파생해야 한다(secret 공유).
둘 다 계약을 키운다.
웹이 파생하면 CTF 서버는 "받은 불투명 문자열을 심는다"만 하면 되고, 그게 수신측 현재 모양(`req.Flag` → `FLAG` env)과 그대로 맞는다.

이 선택은 ADR-0016·0018의 경계와도 맞는다.
리소스(인스턴스·파일)는 CTF가 맡고, 사람·점수·정답은 웹이 맡는다.
CTF 서버는 플래그 내용을 모른 채 불투명 문자열로만 다룬다.

**파생 방식은 이 ADR이 정하지 않는다.**

HMAC으로 `code+uuid`에서 뽑든, 랜덤으로 만들어 웹 DB에 저장하든 웹의 구현 선택이다.
CTF 서버는 그 값을 해석하지 않으므로 방식에 의존하지 않는다.

### 계약 범위

이 ADR이 양쪽에 거는 계약은 셋이다.

* **웹 → provisioner 메시지에 `flag` 필드를 더한다.** DYNAMIC일 때만 채운다. STATIC이면 비운다(정답은 이미지에 구워져 있다).
* **provisioner는 `queue.Request.flag`를 `protocol.RequestMessage.Flag`에 그대로 복사한다.** 내용을 보지도, 만들지도 않는다.
* **값이 없으면 실패로 돌린다.** `flagType=DYNAMIC`인데 `flag`가 비어 오면 provisioner가 FAILED로 사유를 남긴다. 빈 플래그 컨테이너를 조용히 띄우지 않는다. 반대로 STATIC인데 `flag`가 차 있으면 무시한다(이미지 플래그가 정답이다).

마지막 항목은 추천 기본값이다.
웹이 계약을 어긴 경우(DYNAMIC인데 flag 누락)를 CTF가 조용히 넘기면, 모두에게 같은 빈 플래그가 박힌 DYNAMIC 문제가 떠서 알아보기 어려운 모양으로 터진다.
그래서 일찍, FAILED 사유로 드러낸다(`queue.Request` 필수 필드 검증과 같은 태도다).

### 보안

평문 플래그가 두 redis와 provisioner 메모리를 지난다.

* `ctf-redis`는 내부망이다(ADR-0012). 대외로 안 열린다.
* provisioner와 agent는 **로그에 flag를 찍지 않는다.** 지금 로그에 flag가 없는데, 전달을 붙이면서 실수로 넣지 않도록 못 박는다.
* 컨테이너 안에서 자기 `FLAG` env가 보이는 건 문제가 아니다. 원래 자기 플래그다. DYNAMIC의 핵심은 남의 플래그를 못 본다는 것이고, 그건 참가자마다 컨테이너가 다른(PER_USER) 구조가 지킨다.

## 3. Consequences (결과 및 영향)

### 🟢 장점

* **구현이 가장 작다.** 수신측(agent)은 이미 완성돼 있다. provisioner는 필드 하나를 복사한다. 파생은 웹이 맡는다.
* **관심사가 깨끗이 갈린다.** CTF 서버는 플래그 정답을 모른다. secret 공유도, 결과 왕복도 없다.
* **STATIC/DYNAMIC 분기가 메시지 한 필드로 끝난다.** CTF 서버 쪽엔 파생 로직이 아예 없다.

### 🔴 단점 / 개선안

* **평문 플래그가 redis와 provisioner를 지난다.** 내부망과 로그 금지로 막지만 암호화된 비밀은 아니다. 지금 규모(내부 수십 명)에선 과하지 않은 수준이다. 더 조이려면 provisioner가 받은 즉시 메모리로만 들고 `ctf-redis` 구간을 짧게 가져가는 개선을 나중에 본다.
* **웹 구현이 계약을 지켜야 한다.** DYNAMIC인데 flag를 빠뜨리면 FAILED다. CTF가 막아주긴 하지만, 플래그를 "채워 보내는" 책임 자체는 웹에 있다. 이 ADR과 짝이 되는 웹 레포 이슈가 필요하다.
* **파생 방식 변경이 웹 안에서 끝나는 대신, 웹이 그걸 혼자 책임진다.** CTF는 모르니 파생이 잘못돼도 CTF 로그로는 못 잡는다.

## 4. 참고자료

* [ADR-0012](<./[ADR-0012] ctf 서버 컨테이너 간 통신 방식 확정.md>) — ctf-redis·nimda-redis 통 분리, provisioner만 두 망에 걸침
* [ADR-0016](<./[ADR-0016] 인스턴스 접근 인가 — 게이트웨이는 판정하지 않는다.md>) — 사람은 웹, 리소스는 CTF
* [ADR-0017](<./[ADR-0017] 문제 컨테이너 격리 경계 — 도메인·네트워크·내부 API.md>) — 적대적 프로세스 전제
* [ADR-0018](<./[ADR-0018] 인증 인가 위치 확정.md>) — 인증·인가는 웹
* [ADR-0019](<./[ADR-0019] 공유 인스턴스(SHARED) — 요청이 아니라 문제에 묶고, 수명은 참가 임대로 정한다.md>) — SHARED는 DYNAMIC 불가
* 관련 코드: `protocol/message.go`(Flag), `agent/internal/agentserver/handler.go`(FLAG env 주입), `provisioner/internal/handler/dispatch.go`(전달), `provisioner/internal/queue/message.go`(웹 메시지)
