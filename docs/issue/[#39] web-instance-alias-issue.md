# [#39][agent] web 인스턴스 접속 이름을 63자 이하로 — ctf-instances 네트워크 alias

> 선행: #35(web 인스턴스를 ctf-instances로, host=컨테이너 이름). ADR-0017 2-2 참고합니다.
> 해당 이슈는 **agent가 web 응답에 돌려주는 host 이름의 길이**를 다룰 예정입니다.
> #35 작업 중이면 #35에 같이 넣어도 됩니다. 컨테이너 이름 규칙 자체(`ctf-{code}-{uuid}`)는 안 바꿉니다.

### 배경

#35 이후 web 응답의 host는 컨테이너 이름이다. 웹 백엔드는 그 이름을 그대로 도커 embedded DNS로 풀어서 붙는다.

```
프록시 ──▶ http://ctf-{code}-{uuid}:{EXPOSE 포트}
```

문제는 이 이름이 **점 없는 한 덩어리라 DNS 라벨 하나**가 된다는 것이다. DNS 라벨은 최대 63바이트다(RFC 1035).

```
ctf-(4) + code + -(1) + uuid(36) = 41 + len(code)
```

`sanitize`는 code를 `[a-z0-9-]`로 거르지만 길이는 안 자른다. provisioner의 `challengeCodePattern`은 50자까지 허용한다.
**code가 22자를 넘으면 이름이 63자를 넘는다.**

웹 레포에서 로컬로 확인했다(2026-10-28).

| 이름 길이 | 컨테이너 | backend 이미지에서 `curl http://{이름}:80/` |
|---|---|---|
| 53자 (`ctf-web-basic-01-{uuid}`) | 뜸 | 200 |
| 63자 | 뜸 | 200 |
| 64자 | **뜸** | **실패 (exit 6, 이름 해석 안 됨)** |

도커는 64자 이름도 그대로 만들어 준다. 컨테이너는 정상으로 떠 있고 READY 응답도 나가는데 웹 백엔드만 502를 낸다.
떠 있는 걸 보고 원인을 찾기 어려운 종류의 버그다.

### 설계

**1. ctf-instances에 붙일 때 짧은 alias를 준다**

도커는 네트워크마다 컨테이너에 별칭(alias)을 줄 수 있고, embedded DNS는 별칭도 풀어준다.

```go
netCfg.EndpointsConfig = map[string]*network.EndpointSettings{
    "ctf-instances": {Aliases: []string{"i-" + uuid}},
}
```

* `i-{uuid}`는 38자다. code 길이와 상관없이 63자 안에 든다.
* SHARED는 `ctf-{code}` 하나를 여러 uuid가 같이 쓴다. alias는 `s-{code}`처럼 code만으로 둔다(최대 52자).

**2. web 응답 host를 alias로 돌려준다**

`Create`, `Existing` 둘 다. 이름 규칙(`ctf-{code}-{uuid}`)은 registry·reaper·라벨이 쓰니까 그대로 두고, 밖으로 나가는 host만 바꾼다.

**왜 이름을 줄이지 않나?**

이름은 "같은 이름 = 같은 인스턴스"(재배달·중복 요청 방지)의 기준이고 reaper도 이름으로 찾는다.
이름을 바꾸면 기존 동작 전체를 다시 봐야 한다. alias는 네트워크 접속용 이름만 따로 주는 거라 영향이 작다.

### 작업

* [ ] `runtime/docker.go`: ctf-instances 부착 시 `Aliases` 설정 (PER_USER `i-{uuid}`, SHARED `s-{code}`)
* [ ] `agentserver/handler.go`: web `Create`/`Existing` 응답 host = alias
* [ ] alias 길이 63자 검증 (넘으면 FAILED로 사유를 남긴다)
* [ ] 테스트: code 30자 문제로 인스턴스 생성 → 같은 네트워크에서 alias로 HTTP 200

### 완료 조건

* [ ] code 길이와 상관없이 web 응답 host가 63자 이하다
* [ ] 같은 네트워크의 다른 컨테이너에서 그 host로 HTTP 응답을 받는다
* [ ] 재배달(`Existing`) 응답의 host도 같은 alias다
* [ ] pwn(TCP) 경로는 바뀌지 않는다

### 이번에 하지 않는 것

* **challengeCode 길이 제한 강화.** 50자 허용은 다운로드 등 다른 경로에서도 쓰니 그대로 둔다.
* **이름 규칙 변경.** 위 이유로 손대지 않는다.
