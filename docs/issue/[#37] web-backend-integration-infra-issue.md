# [#37][infra] 웹 백엔드 연동 운영 설정 — 네트워크·Redis 비밀번호·배포 순서

> 선행: #35(web 인스턴스를 ctf-instances로). 웹 레포 `docs/runbook/[RB-0001] CTF 웹 인스턴스 프록시 운영 배포.md` 참고합니다.
> 해당 이슈는 **코드가 아니라 운영 설정**(도커 네트워크·provisioner 환경변수·배포 순서)을 다룰 예정입니다.
> 웹 쪽 서버 작업(DNS·인증서·nginx)은 웹 RB-0001이 다룹니다. 여기는 CTF 서버 쪽만 정리합니다.

### 배경

웹 레포에서 서브도메인 프록시를 배포 형태로 맞추는 작업을 했다(compose·nginx·RB-0001).

**1. external `nimda-network` 이름이 운영에서 달랐다**

Judge compose는 `nimda-network`를 external로 찾는다. 웹 운영 compose는 이름을 고정하지 않아서 실제 이름이 `{프로젝트}_nimda-network`였다.
웹 쪽에서 `name: nimda-network`로 고정했다(웹 RB-0001 R5). 서버에서 웹 스택을 한 번 다시 띄운 뒤에 provisioner를 붙여야 한다.

**2. ctf-instances를 누가 언제 만드나**

#35에서 agent가 web 인스턴스를 `ctf-instances`에 붙이고, 웹 backend도 같은 네트워크에 external로 붙는다.
**네트워크가 없으면 양쪽 다 실패한다.** agent는 컨테이너 생성이 실패하고, 웹 backend는 컨테이너 자체가 안 뜬다.
그래서 Judge·웹 어느 쪽 배포보다 먼저 만들어야 한다.

**3. 같은 호스트**

도커 네트워크는 호스트 단위다. agent가 만든 인스턴스와 웹 backend가 **같은 도커 호스트**에 있어야 `ctf-instances`로 묶인다.
CTF 서버를 다른 EC2로 옮기면 이 구조가 통째로 안 된다(그때는 오버레이 네트워크나 내부 라우팅을 다시 설계해야 한다).

### 설계

**배포 순서**

```
1. docker network create --internal ctf-instances      (없으면)
2. 웹: nimda-network 이름 고정 반영 + 스택 재기동         (웹 RB-0001 단계 3)
3. 웹: backend 배포                                     (ctf-instances에 붙음)
4. Judge: provisioner/.env에 REDIS_PASSWORD 추가
5. Judge: #35 배포 (agent·provisioner 재기동)
```

웹 backend는 #35보다 먼저 배포해도 된다. 인스턴스가 아직 `127.0.0.1`로 오면 지금처럼 접속이 안 될 뿐이다.

### 작업

* [ ] 운영 서버: `ctf-instances` 생성, provisioner `.env` 반영

### 완료 조건

* [ ] provisioner 로그에 nimda-redis 인증 오류가 없고 요청 스트림을 읽는다
* [ ] `docker network inspect nimda-network`에 웹 컨테이너와 ctf-provisioner가 같이 있다
* [ ] `docker network inspect ctf-instances`에 웹 backend와 web 인스턴스가 같이 있다
* [ ] 웹에서 인스턴스 생성 → READY → `https://inst-{uuid}.nimda.kr` 접속 (웹 RB-0001 5절)

### 이번에 하지 않는 것

* **CTF 서버 분리(다른 호스트).** 위 4번 때문에 구조부터 다시 봐야 한다. 지금은 같은 호스트를 전제로 한다.
* **비밀번호 한 곳에서 관리.** secrets 관리 도구는 별도로 본다.
