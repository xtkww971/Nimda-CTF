* **날짜:** 2026-10-23
* **작성자:** 이도현
* **상태:** 제안됨 (기획, 아직 구현 안 함)

## 1. Context (배경)

web 문제는 PER_USER와 SHARED 둘 다 만들어져 있다.
참가자는 `inst-{uuid}` 서브도메인으로 웹 프록시를 거쳐 컨테이너에 붙는다(ADR-0011, ADR-0017).
이제 남은 건 pwn(포너블)이다.

pwn은 지금까지 계속 미뤄왔다.
ADR-0011, 0013, 0015, 0017 전부 "pwn은 따로 다룬다"로 끝난다.
이 문서가 그 숙제를 정리한다.

### pwn 문제가 무엇인가

pwn은 서버에서 돌고 있는 프로그램의 버그를 이용해서 그 서버의 셸을 따는 문제다.
셸을 따면 서버 안에 있는 `flag` 파일을 읽어서 제출한다.

참가자가 받는 건 보통 세 가지다.

* 문제 바이너리 (예: `chall`, C로 만든 리눅스 실행 파일)
* 서버에서 쓰는 libc (`libc.so.6`). exploit이 libc 주소를 계산해야 해서 같은 버전이 필요하다.
* 접속 주소 `nc pwn.example.kr 30042`

참가자는 바이너리를 자기 컴퓨터에서 분석하고, exploit 스크립트를 짜서 서버에 보낸다.
스크립트는 거의 다 pwntools(파이썬 라이브러리)로 짠다.

```python
from pwn import *
p = remote("pwn.example.kr", 30042)   # TCP 연결
p.sendline(b"A" * 40 + p64(0x401196)) # 버퍼 오버플로로 리턴 주소를 덮는다
p.interactive()                       # 셸이 열리면 직접 명령을 친다
```

여기서 중요한 건 **참가자가 브라우저가 아니라 자기 터미널에서 raw TCP로 붙는다**는 점이다.
HTTP가 아니라서 Host 헤더도 없고, 지금 만든 서브도메인 프록시를 그대로 쓸 수 없다.

### 서버 입장에서 pwn은 무엇이 다른가

**문제 프로그램이 네트워크 서버가 아니다.**

web 문제는 Flask나 PHP 같은 웹 서버가 포트를 열고 기다린다.
pwn 바이너리는 그냥 stdin으로 읽고 stdout으로 쓰는 프로그램이다.
그래서 `socat`이나 `xinetd` 같은 도구가 포트를 열고, 연결이 들어올 때마다 바이너리를 하나씩 실행해서 소켓을 stdin/stdout에 이어준다.

```
socat TCP-LISTEN:31337,reuseaddr,fork EXEC:/home/ctf/chall,stderr
```

`fork` 옵션 때문에 연결마다 새 프로세스가 뜬다.
exploit에 실패해서 프로그램이 죽어도 그 연결의 프로세스만 죽고, 다음 연결은 새 프로세스를 받는다.

**셸을 따는 게 정답이다.**

ADR-0017에서 문제 컨테이너를 "뚫리라고 만든 프로세스"라고 적었다.
pwn은 이게 제일 극단적이다.
참가자가 컨테이너 안에서 임의 명령을 실행하는 게 문제의 정답이다.
그래서 pwn은 SHARED로 내면 안 된다.
한 명이 셸을 얻어서 `rm flag`를 하거나 프로세스를 죽이면 모두의 문제가 망가지기 때문이다.

### 이미 정해진 것

* **pwn은 PER_USER다.** 로드맵 T3, ADR-0019에서 정했다.
* **provisioner는 이미 `PWN` 카테고리를 인스턴스 생성으로 보낸다.**
  `provisioner/internal/handler/task.go`의 `instanceCategories`에 `"PWN": true`가 있다.
  그래서 지금도 PWN 요청이 오면 web과 똑같이 컨테이너가 뜬다.
  다만 포트가 `127.0.0.1`에만 열려서 밖에서 붙을 수 없다.
* **web 문제 포트는 밖에 안 연다(ADR-0017).** 80/443만 연다.
* **agent는 문제 유형을 모른다.** `CREATE`와 isolation type만 보고 동작한다(dispatcher 주석).
* **TTL은 reaper가 집행한다(ADR-0014).**

### 접속 방법은 어떤 게 있을까?

ADR-0011과 ADR-0015에서 두 갈래를 적어뒀다.
여기에 하나를 더해서 세 가지를 비교했다.

**(A) 포트 풀에서 포트를 하나 열어준다 (ADR-0013)**

`30000-30199` 중 하나를 인스턴스에 주고 밖에 연다.
참가자는 `nc host 30042`로 바로 붙는다.
Dreamhack이 이 방식이다(`host3.dreamhack.games:12345`).

**(B) 웹 터미널 (xterm.js + WebSocket ↔ TCP)**

브라우저에 터미널을 띄우고, 웹 프록시가 WebSocket을 컨테이너 TCP에 이어준다.
80/443만 열면 된다는 게 장점이다.

**(C) TCP 게이트웨이 한 개 + 토큰**

포트 하나(예: `31337`)만 열고, 접속하면 첫 줄에 토큰을 보내게 한다.
게이트웨이가 토큰을 보고 어느 컨테이너로 이어줄지 정한다.

### 웹 터미널(B)로 하면 포트를 안 열어도 되지 않을까?

포트를 안 여는 건 맞다.
하지만 pwn을 실제로 푸는 방식과 맞지 않는다.

참가자는 터미널에 손으로 입력하지 않는다.
pwntools 스크립트가 `\x00`, `\xff` 같은 바이트를 정확히 보내야 하고, 서버가 출력한 주소를 읽어서 다음 입력을 계산한다.
브라우저 터미널에서는 이 스크립트를 돌릴 방법이 없다.
결국 참가자가 WebSocket ↔ TCP 변환 도구(`websocat` 같은)를 따로 깔아야 한다.

그러면 "브라우저에서 바로 된다"는 장점이 사라지고, 웹 프록시에 WebSocket 처리만 늘어난다.

### TCP 게이트웨이(C)는?

포트를 하나만 연다는 점은 좋다.
하지만 접속하자마자 토큰을 보내야 해서, 참가자가 짠 exploit 스크립트를 우리 플랫폼 전용으로 고쳐야 한다.
다른 CTF에서 쓰던 스크립트를 그대로 못 쓴다.

그리고 ADR-0015에서 게이트웨이를 내려놓은 이유(부품 추가, 단일 장애점)가 그대로 돌아온다.
이번에는 HTTP도 아니라서 기성 프록시로 대체하기도 어렵다.

### 포트를 열면 남의 인스턴스에 붙을 수 있지 않을까?

맞다.
B가 `30000-30199`를 전부 스캔하면 A의 인스턴스에도 붙을 수 있다.
web은 웹 프록시가 매 요청 소유권을 확인하지만(ADR-0016), raw TCP에는 그 검사를 끼울 자리가 없다.

그런데 pwn에서는 이게 생각보다 위험하지 않다.

* **A의 인스턴스를 뚫는 건 자기 인스턴스를 뚫는 것과 난이도가 같다.**
  같은 이미지에서 뜬 같은 바이너리다.
  A의 인스턴스에 붙어서 얻을 수 있는 건, 자기 인스턴스에서도 똑같이 얻을 수 있다.
* **A의 셸을 가로챌 수 없다.** `socat fork`라서 B가 붙으면 B 전용 새 프로세스가 뜬다.
  A가 열어둔 셸에 B가 끼어드는 일은 없다.
* **DYNAMIC 플래그면 훔친 플래그가 쓸모없다.** A의 플래그를 B가 제출하면 오답이다.
  오히려 "남의 플래그를 냈다"는 기록이 남아서 부정행위를 잡을 수 있다.

남는 위험은 A의 인스턴스를 망가뜨리는 것(플래그 파일 삭제, 프로세스 종료)이다.
A는 인스턴스를 다시 만들면 된다.
동아리 내부 상시 워게임에서는 감수할 만하다고 본다.

## 2. Decision (결정)

**pwn은 포트 풀(A)로 간다.**
인스턴스마다 `30000-30199` 중 포트 하나를 열어주고, 참가자는 `nc`나 pwntools로 직접 붙는다.
ADR-0013에서 설계만 해둔 포트 풀을 pwn에 한해서 실제로 만든다.

web은 지금처럼 포트를 밖에 안 연다.
그래서 "80/443만 연다"는 ADR-0017의 원칙은 **web에 대해서만** 유지되고, pwn용으로 `30000-30199/tcp` 범위가 추가로 열린다.

### agent는 문제 유형 대신 "어떻게 접속하는가"를 받는다

agent는 문제 유형을 모르게 만들어뒀다.
여기서 `PWN`을 agent에 보내면 그 원칙이 깨진다.
그래서 유형 대신 **접속 방식**을 메시지에 싣는다.

```go
type AccessType string

const (
    AccessHTTP AccessType = "HTTP" // 웹 프록시를 거친다. 포트는 loopback/내부망에만
    AccessTCP  AccessType = "TCP"  // 포트 풀에서 포트를 받아 밖에 연다
)
```

* **누가 정하나:** provisioner가 `challengeCategory`를 보고 정한다. `PWN → TCP`, `WEB → HTTP`.
  provisioner는 이미 카테고리로 분기하고 있어서 한 줄 매핑이 늘어날 뿐이다.
  웹은 메시지를 바꾸지 않아도 된다.
* **검증:** isolation type과 똑같이 두 값 외에는 reject한다.
  잘못된 값이 들어왔을 때 포트가 밖에 열리는 것보다 아예 안 뜨는 게 안전하다.
* **TCP + SHARED는 reject한다.** pwn이 SHARED로 뜨는 걸 메시지 단계에서 막는다.
  provisioner에서도 `PWN + SHARED`를 한 번 더 막는다.
  (nc로 붙는 stateless한 crypto 문제는 SHARED여도 되지만, 그건 그 문제를 붙일 때 다시 연다.)

**`challengeCategory`를 그냥 agent에 넘기면 안 될까?**

카테고리는 계속 늘어난다(WEB, PWN, CRYPTO, MISC ...).
agent가 카테고리를 알면 새 카테고리가 생길 때마다 agent도 고쳐야 한다.
agent가 실제로 알아야 하는 건 "포트를 밖에 여는가"뿐이다.
그 질문에 맞는 값만 받는 게 맞다.

### 포트 풀은 agent가 관리한다 (ADR-0013 그대로)

```
ctf:ports:free          (Set)    아직 안 나간 포트
uuid:{uuid}             (Hash)   기존 필드 + port
```

ADR-0013에는 `ctf:ports:taken:{port}`도 있었는데 뺀다.
"이 포트를 누가 쓰나"는 컨테이너 라벨 `nimda.ctf.port`로 알 수 있기 때문이다.
같은 정보를 두 군데 저장하면 둘이 어긋나는 경우를 또 처리해야 한다.

* **할당:** `SPOP ctf:ports:free`. 원자적이라 lock이 필요 없다.
  `SPOP`은 랜덤으로 꺼내서, 포트 번호로 다음 사람 인스턴스를 짐작하기도 어렵다.
* **풀이 비면:** `FAILED`를 반환한다. "지금 자리가 없다"는 뜻이다.
  ADR-0013에서 말한 대로 풀 크기가 곧 동시 pwn 인스턴스 상한이 된다.
* **반납:** DESTROY와 reaper에서 컨테이너를 지운 뒤 `SADD`로 돌려준다.
* **실행 실패:** `SPOP` 뒤에 컨테이너가 안 뜨면 그 자리에서 바로 돌려준다.

**agent가 재시작하면 풀은 어떻게 될까?**

풀이 Redis에 있어서 재시작해도 남아 있다.
다만 반납 전에 죽는 경우가 반드시 생기니까, agent가 시작할 때 풀을 다시 계산한다.

```
free = {30000..30199} - {실행 중인 nimda.ctf 컨테이너의 nimda.ctf.port 라벨}
```

도커에 실제로 떠 있는 컨테이너가 기준이다.
Redis 값이 어떻게 틀어져 있든 시작할 때 한 번 맞춰진다.
reaper 주기마다 같은 계산을 하면 운영 중에 새는 포트도 잡을 수 있다.

### 컨테이너 실행은 web과 세 군데가 다르다

`runtime.Run`에 옵션을 받아서 나눈다.

| | HTTP (web) | TCP (pwn) |
| --- | --- | --- |
| 호스트 포트 | 데몬이 고른 loopback 임의 포트 (이후 ADR-0017로 제거) | 풀에서 받은 포트 |
| 바인딩 IP | `127.0.0.1` | `CTF_PWN_BIND_IP` (기본 `0.0.0.0`) |
| 네트워크 | `ctf-instances` (웹 프록시가 같이 붙음) | `ctf-pwn` (아무도 같이 안 붙음) |
| 응답 `host` | 프록시가 쓰는 내부 주소 | 참가자에게 보여줄 주소 `CTF_PWN_PUBLIC_HOST` |

**pwn을 `ctf-instances`에 같이 두면 안 될까?**

ADR-0017은 `ctf-instances`를 `internal: true`로 만들어서 인터넷을 막기로 했다.
그런데 internal 네트워크에 붙은 컨테이너는 포트를 호스트에 publish할 수 없는 것으로 알고 있다.
(문서 기준이고 직접 확인은 아직 안 했다.)
그러면 pwn은 밖에서 붙을 방법이 없어진다.

그래서 pwn용 `ctf-pwn` bridge를 따로 만든다.

* **internal은 끈다.** 포트 publish를 해야 하기 때문이다.
* **나가는 연결은 `DOCKER-USER` iptables 규칙으로 막는다.**
  `ctf-pwn` 서브넷에서 나가는 새 연결은 drop하고, 이미 맺어진 연결(참가자가 들어온 연결)의 응답만 허용한다.
  pwn은 셸을 따는 게 정답이라 reverse shell이나 외부 공격 발판으로 쓰이는 걸 꼭 막아야 한다.
* **컨테이너끼리 통신을 끈다(`enable_icc=false`).**
  web 네트워크는 웹 프록시가 같이 붙어야 해서 이걸 못 켰다(ADR-0017 단점).
  `ctf-pwn`에는 같이 붙을 게 없어서 바로 끌 수 있다.
  셸을 딴 참가자가 내부망으로 옆 인스턴스를 스캔하는 길이 막힌다.

### 플래그는 파일로 둔다

pwn 문제는 셸을 따서 `cat flag`를 하는 구조라 플래그가 파일이어야 한다.

* **STATIC:** 출제자가 이미지에 `flag` 파일을 넣는다. agent가 할 일은 없다.
* **DYNAMIC:** 지금처럼 env `FLAG`로 넣고, 컨테이너 시작 스크립트가 파일로 옮긴 뒤 env를 지운다.
  env로 남겨두면 `/proc/self/environ`이나 `env` 명령으로 읽혀서 문제 의도와 달라진다.

pwn은 PER_USER라서 DYNAMIC을 쓸 수 있다.
위에서 말한 "남의 인스턴스에 붙는 문제"를 생각하면 **pwn은 DYNAMIC을 권장한다.**

### 출제자가 지켜야 하는 규칙

agent는 컨테이너를 `CapDrop: ALL`, `no-new-privileges`로 띄운다.
pwn 문제는 이 조건에서 돌아가야 한다.

* **바이너리는 root가 아닌 사용자로 돈다.** Dockerfile에 `USER ctf`를 쓴다.
  `CapDrop: ALL`이라 컨테이너 안에서 `su`로 사용자를 바꿀 수 없기 때문이다.
* **setuid 바이너리에 기대지 않는다.** `no-new-privileges`면 setuid가 동작하지 않는다.
  일부 문제는 "셸은 따도 `/readflag`(setuid)로만 플래그를 읽게" 만드는데, 그런 문제는 지금 구조로 못 낸다.
* **`EXPOSE`는 하나만 쓴다.** agent는 첫 번째 EXPOSE 포트만 연다.
* **socat 같은 도구로 연결마다 fork한다.** 그래야 한 번 죽어도 다음 연결이 산다.

위 규칙을 지킨 Dockerfile 예시는 `docs/features/PwnInstance.md`에 적었다.

### TTL은 컨테이너를 지우면 집행된다

ADR-0015에서 "방화벽 규칙을 지워도 이미 붙은 연결은 안 끊긴다"를 문제로 봤다.
포트 풀은 방화벽을 건드리지 않는다.
보안 그룹에 `30000-30199/tcp`를 한 번 열어두고 끝이다.

만료는 reaper가 컨테이너를 지우는 걸로 집행한다.
컨테이너가 없어지면 그 안의 socat과 셸도 같이 죽는다.
참가자 쪽 연결도 끊긴다.
그래서 ADR-0015에서 걱정한 "규칙을 지웠는데 세션이 5일 남는" 문제가 생기지 않는다.

### 작업 순서

모듈 하나에 작업 하나 규칙에 따라 나눈다.

1. `protocol`: `AccessType` 타입, 값 검증, TCP + SHARED reject (계약)
2. `provisioner`: `challengeCategory → accessType` 매핑, PWN + SHARED reject
3. `agent/internal/registry`: 포트 풀 할당·반납·재계산, `uuid:` hash에 `port` 필드
4. `agent/internal/runtime`: `Run` 옵션 (지정 포트, 바인딩 IP, 네트워크), `nimda.ctf.port` 라벨
5. `agent/internal/agentqueue`: CREATE, DESTROY의 TCP 분기
6. reaper: 포트 반납과 풀 재계산 (ADR-0014 작업에 얹는다)
7. 인프라: `ctf-pwn` 네트워크, `DOCKER-USER` egress 규칙, 보안 그룹 `30000-30199/tcp`
8. 테스트 문제: `test-problems/03-pwn-bof`
9. 웹: PWN 문제는 링크 대신 `nc host port`를 보여준다

3번은 1번이 끝나야 하고, 5번은 3·4번이 끝나야 한다.
7번은 코드와 상관없어서 아무 때나 해도 된다.
8번은 먼저 만들어두면 3~6번을 검증할 때 바로 쓸 수 있다.

## 3. Consequences (결과 및 영향)

### 🟢 장점

* **참가자는 평소 쓰던 스크립트를 그대로 쓴다.** `remote(host, port)` 한 줄이면 된다.
  다른 CTF나 Dreamhack에서 쓰던 방식과 같다.
* **부품이 늘지 않는다.** 게이트웨이도 WebSocket 브리지도 없다. 도커 포트 publish와 Redis Set 하나다.
* **ADR-0013 설계를 그대로 쓴다.** 포트 풀 설계를 새로 하지 않는다.
* **동시 pwn 인스턴스 상한이 자동으로 생긴다.** 풀 크기가 상한이다.
* **만료가 확실하게 집행된다.** 컨테이너를 지우면 연결도 같이 끊긴다.
* **agent는 계속 문제 유형을 모른다.** "포트를 밖에 여는가"만 안다.
* **pwn 네트워크는 web보다 더 잘 격리된다.** 같이 붙을 프록시가 없어서 `enable_icc=false`를 바로 쓸 수 있다.

### 🔴 단점 / 개선안

**남의 인스턴스에 붙을 수 있다**

위에서 정리한 대로 얻을 수 있는 게 거의 없지만, 망가뜨릴 수는 있다.
대회처럼 경쟁이 붙으면 문제가 된다.
그때는 (C)처럼 접속 직후 토큰을 확인하는 얇은 TCP 프록시를 앞에 두거나, 참가자 IP로 접근을 제한하는 방법을 다시 본다.

**80/443만 열던 원칙이 깨진다**

`30000-30199/tcp` 200개가 밖에 열린다.
빈 포트로 들어오는 연결은 OS가 RST로 응답한다(ADR-0015에서 걱정한 부분).
동아리 규모에서는 감수하지만, 대량 스캔이 문제가 되면 rate limit을 걸어야 한다.

**출제자에게 규칙이 생긴다**

`USER ctf`, setuid 금지, EXPOSE 하나.
규칙을 안 지킨 문제는 뜨긴 하는데 제대로 동작하지 않는다.
나중에 agent가 빌드 직후 이미지를 검사해서(USER가 root인지 등) 미리 reject하는 게 좋다.

**gVisor에서 exploit이 다르게 동작할 수 있다**

운영에서는 `runsc`(gVisor)를 쓰기로 했다(ADR-0008 예정).
gVisor는 syscall을 자기가 구현하기 때문에, 일반 커널과 메모리 배치나 동작이 미묘하게 다를 수 있다.
스택 오버플로, ROP, heap 같은 유저 영역 pwn은 대부분 괜찮을 거라고 보지만 확인은 안 했다.
커널 pwn 문제는 gVisor에서 낼 수 없다.
`test-problems/03-pwn-bof`로 `runc`와 `runsc` 둘 다 확인해야 한다.

**확인 안 된 전제가 있다**

* internal 네트워크에서 포트 publish가 안 된다는 것 (그래서 `ctf-pwn`을 따로 둔다)
* `DOCKER-USER` 규칙으로 egress를 막으면서 들어오는 연결 응답은 살릴 수 있는지
* `enable_icc=false`로 같은 네트워크 컨테이너끼리 실제로 막히는지

전부 7번 작업에서 직접 확인한다.

**단일 호스트 전제가 더 굳는다**

ADR-0013 단점 그대로다.
포트 풀은 호스트 하나의 자원이라, agent를 여러 대로 늘리면 "어느 호스트의 몇 번 포트"가 된다.

**인스턴스당 포트 하나다**

pwn 문제가 포트 두 개(예: 바이너리 + 디버그용 서비스)를 열어야 하면 지금 구조로는 못 담는다.

## 4. 참고자료

### 내부 문서

* [ADR-0011](<./[ADR-0011] 웹 문제 인스턴스 상호작용 — 서브도메인 기반 리버스 프록시.md>):
  pwn은 포트 풀 또는 웹 터미널로 따로 다룬다고 남겨둔 문서.
* [ADR-0013](<./[ADR-0013] 인스턴스 포트 관리 — 임의 할당 대신 용도별 분리와 포트 풀.md>):
  포트 풀 설계. 이 문서가 pwn에 한해서 실제로 구현한다. `taken` 키는 라벨로 대체한다.
* [ADR-0014](<./[ADR-0014] 인스턴스 수명(TTL) 정책 — 선언 대신 집행.md>): reaper. 포트 반납이 여기에 얹힌다.
* [ADR-0015](<./[ADR-0015] 인스턴스 접근 경로 — 포트 노출 대신 게이트웨이.md>):
  방화벽으로는 TTL을 집행 못 한다는 근거, 게이트웨이를 내려놓은 이유.
* [ADR-0016](<./[ADR-0016] 인스턴스 접근 인가 — 게이트웨이는 판정하지 않는다.md>): uuid는 비밀이 아니다.
* [ADR-0017](<./[ADR-0017] 문제 컨테이너 격리 경계 — 도메인·네트워크·내부 API.md>):
  `ctf-instances` internal 네트워크, 80/443만 연다는 원칙. pwn이 예외가 되는 지점.
* [ADR-0019](<./[ADR-0019] 공유 인스턴스(SHARED) — 요청이 아니라 문제에 묶고, 수명은 참가 임대로 정한다.md>):
  pwn은 SHARED로 내면 안 된다. isolation type 검증과 같은 방식으로 `AccessType`을 검증한다.
* `docs/features/PwnInstance.md`: 출제자, 참가자, 서버 입장의 시나리오.

### 코드

* `provisioner/internal/handler/task.go`: `instanceCategories`의 `"PWN": true`. accessType 매핑이 들어갈 자리.
* `agent/internal/runtime/docker.go`: `PortBindings`의 `HostIP: "127.0.0.1"`, `HostPort: ""`,
  빈 `NetworkingConfig`, `CapDrop: ALL`, `no-new-privileges`.
* `agent/internal/agentqueue/dispatcher.go`: `create`의 isolation type 분기. TCP 분기가 들어갈 자리.
* `agent/internal/registry/registry.go`: `uuid:` hash. `port` 필드가 추가될 자리.
* `protocol/message.go`: `IsolationType`. `AccessType`을 같은 모양으로 만든다.

### 외부 자료

* [pwntools — remote](https://docs.pwntools.com/en/stable/tubes/sockets.html): 참가자가 쓰는 TCP 연결 API.
  웹 터미널이 pwn에 맞지 않는 이유.
* [socat man page](http://www.dest-unreach.org/socat/doc/socat.html): `TCP-LISTEN`, `fork`, `EXEC`.
* [Docker — Packet filtering and firewalls](https://docs.docker.com/engine/network/packet-filtering-firewalls/):
  `DOCKER-USER` 체인으로 컨테이너 트래픽을 거르는 방법.
* [Docker — bridge network driver options](https://docs.docker.com/engine/network/drivers/bridge/#options):
  `com.docker.network.bridge.enable_icc`.
* [gVisor — Compatibility](https://gvisor.dev/docs/user_guide/compatibility/): gVisor가 지원하는 syscall 범위.
* [CTFd-Whale](https://github.com/frankli0324/ctfd-whale): raw TCP는 정해둔 포트 대역으로 연다. 같은 선택을 한 선례.
