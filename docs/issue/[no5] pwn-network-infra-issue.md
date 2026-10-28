# [no5] pwn 네트워크 격리 (인프라) — ctf-pwn·egress·보안 그룹

> 선행: agent 배선(runtime·dispatcher·main) 완료. ADR-0020, ADR-0017 참고합니다.
> 해당 이슈는 **코드가 아니라 인프라 설정**(도커 네트워크·방화벽·보안 그룹)을 다룰 예정입니다.
> agent 배선 이슈가 "`ctf-pwn` 네트워크가 미리 있다고 가정한다"고 남겨둔 부분을 채웁니다.
> CPU·디스크 상한, read-only rootfs, gVisor 같은 컨테이너 하드닝은 코드라서 별도 이슈로 다룰 예정입니다.

### 배경

agent는 TCP(pwn) 컨테이너를 `ctf-pwn` 네트워크에 붙이고 포트를 밖에 연다.
그런데 그 네트워크가 지금 없다.
그래서 TCP 요청이 오면 컨테이너 생성이 "network ctf-pwn not found"로 실패한다.

이 이슈에서 `ctf-pwn` 네트워크를 만들고, pwn 컨테이너가 밖으로 나가지 못하게 막는다.
pwn은 셸을 따는 게 정답이라, 참가자가 컨테이너 안에서 임의 코드를 실행한다고 전제해야 한다(ADR-0017).
그 코드가 우리 내부로 들어오거나 밖으로 나가는 길을 다 끊는 게 이 이슈의 목적이다.

**web과 왜 네트워크를 나누나?**

web은 ADR-0017에서 `ctf-instances`(internal) 네트워크에 두기로 했다.
internal 네트워크는 인터넷을 막는 대신 포트를 밖에 publish할 수 없다.
pwn은 포트를 밖에 열어야 하므로 internal을 못 쓴다.
그래서 pwn 전용 `ctf-pwn`을 따로 두고, internal 대신 방화벽으로 밖을 막는다.

### 설계

**1. `ctf-pwn` 네트워크를 만든다**

docker-compose에 선언한다. 다만 이름에 함정이 있다.

**compose 네트워크 이름에 프로젝트명이 붙는다**

`nimda-network`가 `external: true`인 것과 같은 문제다.
compose는 네트워크 이름 앞에 프로젝트명을 붙여 `judgeserver_ctf-pwn`으로 만든다.
그런데 agent는 `CTF_PWN_NETWORK=ctf-pwn`으로 이름을 그대로 쓴다.
이름이 어긋나면 컨테이너를 못 붙인다.

그래서 `name: ctf-pwn`으로 이름을 못박는다.

```yaml
networks:
  ctf-pwn:
    name: ctf-pwn
    driver: bridge
    driver_opts:
      com.docker.network.bridge.enable_icc: "false"
```

* `enable_icc: false`로 컨테이너끼리 통신을 막는다. 셸을 딴 참가자가 옆 인스턴스를 스캔하는 길이 막힌다.
  `ctf-instances`는 프록시가 같이 붙어야 해서 이걸 못 켰지만(ADR-0017), `ctf-pwn`에는 같이 붙을 게 없다.
* `internal`은 켜지 않는다. 포트를 밖에 publish해야 하기 때문이다. 밖으로 나가는 건 방화벽으로 막는다(아래).

agent 컨테이너 자신은 이 네트워크에 붙이지 않는다. agent는 도커 소켓으로 컨테이너를 만들 뿐이다.

**2. 나가는 연결을 막는다 (egress)**

pwn 컨테이너가 인터넷으로 새 연결을 맺지 못하게 한다. reverse shell, 외부 공격 발판, 채굴을 막는다.

`DOCKER-USER` iptables 체인에 규칙을 넣는다. 이 체인은 도커가 지우지 않는다.

```
# ctf-pwn 서브넷에서 나가는 "새" 연결은 버린다. 참가자가 들어온 연결의 응답(established)만 허용한다.
iptables -I DOCKER-USER -s <ctf-pwn 서브넷> -m conntrack --ctstate NEW -j DROP
```

서브넷은 `ctf-pwn`을 만든 뒤 `docker network inspect ctf-pwn`으로 확인해 고정한다.

**3. 호스트 내부 서비스 접근을 막는다**

이게 egress보다 더 급하다. `DOCKER-USER`는 FORWARD 체인이라 컨테이너에서 **호스트로** 가는 연결(INPUT)은 거르지 못한다.
셸을 딴 참가자가 bridge gateway IP(예: `172.x.0.1`)로 ctf-redis에 붙으면, 포트 풀과 인스턴스 기록을 조작할 수 있다.

```
# ctf-pwn 서브넷이 호스트로 들어오는 연결을 막는다 (INPUT 체인)
iptables -I INPUT -s <ctf-pwn 서브넷> -j DROP
```

ctf-redis가 호스트 포트로 열려 있지 않은지도 같이 확인한다. 지금 compose에서 ctf-redis는 `ctf-internal`에만 있고 포트 매핑이 없어야 한다.

**4. 클라우드 메타데이터(IMDS)를 막는다**

`169.254.169.254`로 EC2 IAM 자격증명을 꺼내는 길을 끊는다. provisioner의 S3 키가 유출되면 다른 문제의 플래그까지 샌다.

```
iptables -I DOCKER-USER -s <ctf-pwn 서브넷> -d 169.254.169.254 -j DROP
```

EC2는 IMDSv2 전용 + hop limit 1로 바꾼다. 컨테이너에서 오는 요청은 hop이 하나 더 붙어 막힌다.

**5. 보안 그룹을 연다 (배포)**

AWS 보안 그룹 인바운드에 `30000-30199/tcp`를 연다.
방화벽에 한 줄로 적을 수 있는 게 포트 풀을 고정 범위로 둔 이유다(ADR-0013).

### 작업

* [ ] `docker-compose.yml`에 `ctf-pwn` 네트워크 선언 (`name: ctf-pwn`, `enable_icc: false`)
* [ ] `ctf-pwn` 서브넷 확인 후 egress 차단 규칙(`DOCKER-USER`, NEW 상태 DROP) 적용
* [ ] 호스트 접근 차단 규칙(`INPUT`, ctf-pwn 서브넷 DROP) 적용
* [ ] IMDS 차단 규칙 적용 + EC2 IMDSv2/hop limit 설정
* [ ] 보안 그룹 `30000-30199/tcp` 오픈
* [ ] iptables 규칙을 재부팅 후에도 남게 저장 (`netfilter-persistent` 등)
* [ ] runbook에 위 절차와 서브넷 확인 방법을 적는다 (`docs/runbook/`)

### 완료 조건

* [ ] TCP 요청으로 뜬 컨테이너가 `ctf-pwn`에 붙는다 (`docker inspect`로 확인)
* [ ] 컨테이너 안에서 `curl https://example.com`이 안 된다 (egress 차단)
* [ ] 컨테이너 안에서 호스트 gateway IP의 ctf-redis 포트에 못 붙는다
* [ ] 컨테이너 안에서 `169.254.169.254`에 못 붙는다
* [ ] 컨테이너 둘을 띄워 서로 IP로 붙으면 막힌다 (`enable_icc=false`)
* [ ] 참가자가 들어온 연결로는 정상적으로 문제를 풀 수 있다 (established는 허용)
* [ ] 재부팅 후에도 규칙이 남아 있다

### 이번에 하지 않는 것

* **컨테이너 자원 하드닝.** CPU 상한(`NanoCPUs`), 디스크(read-only rootfs + 크기 제한 tmpfs)는 runtime 코드라 별도 이슈다. 지금은 메모리·PID 상한만 있다.
* **gVisor(runsc) 적용.** pwn은 RCE가 전제라 커널 격리가 web보다 중요하다. 런타임 선정은 ADR-0008이고, pwn에 필수로 둘지는 그 작업에서 정한다.
* **연결 수 제한.** socat `max-children`, IP당 동시 연결 제한은 출제자 템플릿·게이트웨이 쪽 이야기다.
* **WEB의 `ctf-instances` 이전.** ADR-0017의 일이다. 이 이슈는 pwn 네트워크만 만든다.
