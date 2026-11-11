# [ADR-0022] CTF 서버 가용성 신호 — provisioner가 heartbeat 키를 쓴다

* **날짜:** 2026-11-11
* **작성자:** 이도현
* **상태:** 승인

## 1. Context (배경)

웹 문제 페이지는 CTF 서버가 꺼져 있어도 [인스턴스 시작] 버튼을 보여준다.
누르면 요청이 웹 redis 스트림(`ctf:request:ops`)에 쌓이고, 아무도 안 읽어서 PENDING에서 멈춘다.
참가자는 서버가 꺼졌는지, 느린 건지 구분하지 못한다.

웹은 왜 CTF 서버 상태를 모를까?
웹은 스트림에 넣기만 하고, 누가 읽는지는 보지 않기 때문이다.
웹과 CTF 서버가 다른 호스트로 갈라진 뒤(웹은 Lightsail, CTF는 EC2)로는 컨테이너 상태를 직접 볼 길도 없다.
둘 사이에 공유하는 건 웹 redis 하나뿐이다.

consumer group 정보(`XINFO CONSUMERS`)로 판단하면 안 될까?
운영 서버에서 확인해 보니, provisioner가 XREADGROUP으로 대기 중인데도 스트림이 비어 있으면 consumer가 0명으로 나왔다.
idle 시간의 의미도 redis 버전마다 달라서 신호로 쓰기 어렵다.

## 2. Decision (결정)

**provisioner가 웹 redis에 heartbeat 키를 주기적으로 쓴다. 웹은 키가 있는지만 본다.**

| 항목 | 값 |
|---|---|
| 키 | `ctf:server:heartbeat` (provisioner `CTF_HEARTBEAT_KEY`, 웹 `ctf.queue.heartbeat-key`) |
| 값 | 쓴 시각, RFC3339 UTC 문자열. 웹은 값을 해석하지 않는다(디버깅용) |
| 주기 | 10초 (`CTF_HEARTBEAT_INTERVAL`) |
| TTL | 30초 (`CTF_HEARTBEAT_TTL`). 주기보다 길어야 한다 |
| 판정 | 키가 있으면 사용 가능, 없으면 사용 불가 |

### provisioner만 살아 있으면 될까?

아니다. **agent도 살아 있을 때만 쓴다.**
agent가 죽으면 provisioner가 요청을 받아도 컨테이너를 못 만든다.
agent는 이미 ctf-redis에 `monitor:snapshot`을 10초마다 TTL 30초로 쓰고 있다(Feature-01).
provisioner는 이 키가 있는지로 agent 생존을 판단한다. agent에 새 기능을 넣지 않는다.

* snapshot 있음 → heartbeat SET (TTL 30초)
* snapshot 없음 또는 ctf-redis 조회 실패 → heartbeat DEL (바로 내린다)

### 종료할 때는 왜 지우지 않을까?

배포로 재시작하는 몇 초 동안 버튼이 사라지면 안 되기 때문이다.
그 사이에 들어온 요청은 스트림에 남고, 다시 뜨면 처리된다. 사용자 입장에선 "사용 가능"이 맞다.
정말 죽었다면 TTL(30초)이 지나서 사라진다.

### 웹은 이 신호를 어디에 쓰나?

* `GET /api/ctf/server-status` → `{ "available": true|false }`. 프론트가 버튼과 안내 문구를 고른다.
* `POST /api/ctf/instance/{code}` → 사용 불가면 503으로 거절한다. 버튼을 숨기는 것만으론 API 직접 호출을 못 막는다.

## 3. Consequences (결과)

🟢 **장점**

* 웹이 CTF 서버 내부(agent, ctf-redis)를 몰라도 된다. 웹 redis 키 하나만 본다(ADR-0012 통 분리 유지).
* agent가 죽은 경우까지 잡는다. 최대 30초 안에 반영된다.
* 꺼진 서버로 요청이 쌓이지 않는다.

🔴 **단점·개선안**

* 최대 30초 늦게 반영된다. 꺼진 직후 30초 안에 들어온 요청은 PENDING에 머문다. 그 요청은 서버가 다시 뜨면 처리된다.
* provisioner와 agent가 살아 있어도 S3나 도커 빌드가 실패할 수 있다. heartbeat는 "요청을 받을 수 있다"까지만 보장한다.
* 키 이름을 양쪽 설정에 따로 적는다. 한쪽만 바꾸면 버튼이 영영 안 보인다. 기본값을 같게 두고, 바꿀 땐 양쪽을 같이 바꾼다.

## 4. 참고자료

* ADR-0012: ctf 서버 컨테이너 간 통신 방식 확정 (ctf-redis / nimda-redis 분리)
* ADR-0014: 인스턴스 수명(TTL) 정책
* Feature-01: 인스턴스 모니터링 (`monitor:snapshot`)
* 웹 레포 `docs/issue/frontend-ctf-ui-issue.md`: 인스턴스 패널 화면 흐름
