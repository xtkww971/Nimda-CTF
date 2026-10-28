# [RB-0002] web 인스턴스 네트워크 구성

* **최초 작성일:** 2026-10-28
* **최종 수정일:** 2026-10-28
* **작성자:** 이도현
* **대상 시스템:** Infra (CTF 서버 호스트)

---

## 1. 개요 및 트리거

web(HTTP) 문제 인스턴스를 처음 켜기 전에 **한 번** 실행한다.
서버를 새로 구축하거나 호스트를 옮길 때도 다시 한다.

이 절차가 하는 일은 둘이다.

* web 인스턴스가 붙을 `ctf-instances` 네트워크를 만든다.
* 그 네트워크를 `internal`로 만들어, 셸을 딴 web 컨테이너가 인터넷으로 못 나가게 한다(reverse shell 차단).

web 인스턴스는 host 포트를 열지 않는다(ADR-0017, #35).
프록시(웹 백엔드)가 같은 네트워크에서 **컨테이너 이름**으로 붙는다.
배경은 ADR-0017, agent 변경은 이슈 #35에 있다.

이 절차를 안 하면 web 컨테이너 생성이 `network ctf-instances not found`로 실패한다.

## 2. 사전 준비 사항 및 리스크

* **필요 권한:** CTF 서버 SSH 접근, 도커 명령 실행 권한.
* **주의사항:**

  > ⚠️ 네트워크 이름은 agent의 `CTF_INSTANCE_NETWORK`와 같아야 한다(기본 `ctf-instances`).
  > compose에는 `ctf-instances`가 external로 선언돼 있어, 이 네트워크가 없으면 compose up이 멈춘다.

  > ⚠️ `internal`로 만들어야 인터넷이 막힌다. 빼먹으면 web 컨테이너가 밖으로 나갈 수 있다.
  > 이미 만든 뒤에는 `internal` 여부를 바꿀 수 없다. 틀리면 지우고 다시 만든다(6절).

## 3. 단계별 실행 절차

### 단계 1. ctf-instances 네트워크 생성

인터넷으로 못 나가는 internal bridge 네트워크를 만든다.

```bash
docker network create --internal ctf-instances
```

### 단계 2. 프록시(웹 백엔드)를 이 네트워크에 붙인다

프록시가 web 인스턴스에 컨테이너 이름으로 붙으려면 같은 네트워크에 있어야 한다.
웹 백엔드 컨테이너를 `ctf-instances`에 연결한다(웹 레포 compose에서 설정).

```bash
docker network connect ctf-instances <웹_백엔드_컨테이너>
```

> 프록시가 이 네트워크에 붙는 것, 도메인 분리, 소유권 검사는 웹 레포의 몫이다(ADR-0017).
> 이 절차는 네트워크를 만들고 agent가 쓸 수 있게 하는 데까지다.

## 4. 검증

### 검증 1. 네트워크가 internal로 만들어졌는지

```bash
docker network inspect ctf-instances -f '{{.Internal}}'
# true 가 나와야 한다
```

### 검증 2. 같은 네트워크에서 이름으로 닿는지

web 인스턴스를 하나 띄운 뒤(또는 테스트 컨테이너로), 같은 네트워크의 다른 컨테이너에서 이름으로 붙어 본다.

```bash
# 예: 떠 있는 web 인스턴스 이름이 ctf-<code>-<uuid>, EXPOSE 포트가 8080일 때
docker run --rm --network ctf-instances busybox wget -q -O- http://ctf-<code>-<uuid>:8080/
```

### 검증 3. 인터넷이 막혔는지

```bash
docker run --rm --network ctf-instances busybox wget -q -O- -T 3 http://example.com/
# 실패해야 한다(internal이라 밖으로 못 나간다)
```

## 5. 참고

* ADR-0017 — 문제 컨테이너 격리 경계(web은 컨테이너 이름으로 붙는다)
* 이슈 #35 — agent가 web 응답에 컨테이너 이름:EXPOSE 포트를 돌려주는 변경
* 이슈 #39 — 컨테이너 이름이 63자를 넘을 때 짧은 alias(후속)
* RB-0001 — pwn(TCP) 네트워크 격리 구성

## 6. 롤백

네트워크를 잘못 만들었으면(예: `internal` 빠뜨림) 지우고 다시 만든다.
네트워크를 쓰는 컨테이너가 없어야 지워진다.

```bash
docker network rm ctf-instances
docker network create --internal ctf-instances
```
