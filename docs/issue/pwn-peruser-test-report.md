# pwn 문제 PER_USER 제공 테스트 보고서

* **날짜:** 2026-10-24
* **작성자:** 이도현
* **대상 문제:** `03-pwn-bof` (S3 `nimda-website` 등록분)
* **관련 문서:** ADR-0020, `docs/features/PwnInstance.md`

## 무엇을 확인했나

웹에서 등록한 pwn 문제를 S3에서 받아, agent가 PER_USER로 띄우는 방식 그대로 컨테이너를 올리고, 참가자가 실제로 풀 수 있는지까지 확인했다.

결론부터 적으면 **PER_USER로 제공할 수 있다.**
한 문제를 서로 다른 사용자 두 명에게 각각 독립 컨테이너로 내줬고, 둘 다 exploit으로 flag를 얻었다.

## 이번 테스트가 다루지 않은 것

**포트 풀, `ctf-pwn` 네트워크, egress 차단, TTL은 확인하지 않았다.**
ADR-0020의 그 작업들은 아직 구현 전이라 테스트할 코드가 없다.

그래서 이번에는 포트를 `30000-30199` 풀이 아니라 web과 똑같이 `127.0.0.1`의 랜덤 포트로 열었다.
"받아서 빌드하고 PER_USER로 뜨는가"까지가 이번 범위다.
밖에서 `nc`로 붙는 접속 경로는 포트 풀을 만든 뒤에 따로 확인한다.

**provisioner·agent 파이프라인을 끝까지 돌리지도 않았다.**
Redis와 웹 백엔드를 다 띄우는 e2e 대신, agent가 하는 일을 그대로 재현했다.
재현 기준은 코드 두 곳이다.

* `agent/internal/agentserver/handler.go` — 이미지 이름 `ctf-chal-{code}:latest`, 컨테이너 이름 `ctf-{code}-{uuid}`, 라벨
* `agent/internal/runtime/docker.go` — `CapDrop: ALL`, `no-new-privileges`, `-m 256m`, `--pids-limit 256`, `127.0.0.1` 랜덤 포트 publish

즉 "문제 자산이 이 파이프라인을 탈 수 있는가"는 확인했고, "파이프라인 배선 자체가 맞는가"는 이번 범위가 아니다.

## 어떻게 했나

1. `provisioner/.env`의 자격증명으로 S3에서 `challenges/03-pwn-bof/03-pwn-bof.zip`을 받았다.
2. 압축을 풀었다. 받아온 `chall.c`와 `Dockerfile`이 로컬 원본과 동일한지 `diff`로 확인했다.
3. 압축 해제 폴더를 빌드 컨텍스트로 이미지를 빌드했다.
4. UUID 두 개(`11111111-aaaa`, `22222222-bbbb`)로 컨테이너를 각각 띄웠다.
5. 두 컨테이너에 각각 붙어 buffer overflow로 `win()`에 점프해 flag를 받았다.
6. 격리와 하드닝을 점검하고 컨테이너를 지웠다.

## 결과

### S3에서 받은 문제가 등록 원본과 같은가

같다.
`chall.c`, `Dockerfile` 모두 로컬 원본과 바이트까지 일치했다.
flag 파일은 `nimda{t3st_pwn_b0f_st4t1c}`였다.

### 빌드가 되는가

된다.
이미지 `ctf-chal-03-pwn-bof:latest`가 빌드됐다. 크기 345MB.
`EXPOSE`는 `31337/tcp` 하나였다. agent의 `exposedPort`가 이 포트를 읽어 열게 된다.

### PER_USER로 두 개가 독립해서 뜨는가

뜬다.
같은 문제를 UUID 두 개로 요청하니 이름도, 포트도, 컨테이너 ID도 전부 다른 컨테이너 두 개가 떴다.

| uuid | 컨테이너 이름 | host 포트 | 컨테이너 ID |
| --- | --- | --- | --- |
| `11111111-aaaa` | `ctf-03-pwn-bof-11111111-aaaa` | `127.0.0.1:63528` | `3b55ca870949` |
| `22222222-bbbb` | `ctf-03-pwn-bof-22222222-bbbb` | `127.0.0.1:63529` | `2f0d61c92ba8` |

A 컨테이너 `/tmp`에 파일을 만들고 B에서 읽으니 없었다.
둘은 rootfs가 분리된 별개 컨테이너다.

### 참가자가 실제로 풀 수 있는가

풀 수 있다.
두 컨테이너 모두 같은 exploit으로 flag가 나왔다.

```
수신: 'bye\nflag:\nnimda{t3st_pwn_b0f_st4t1c}'
RESULT=FLAG_OK
```

### 셸을 따도 flag를 못 지키나

지킨다.
flag는 root 소유 `-r--r--r--`라, 컨테이너 기본 사용자 `ctf`로는 삭제가 막혔다.

```
rm: cannot remove '/chall/flag': Permission denied
```

pwn은 셸을 따는 게 정답이라, 셸을 따도 flag 파일 자체는 건드리지 못하게 해야 한다.
이 문제는 그 조건을 만족한다.

### 하드닝 옵션이 걸리나

걸린다.
`CapDrop=[ALL]`, `SecurityOpt=[no-new-privileges]`, `Memory=268435456`(256m), `PidsLimit=256`.
컨테이너 기본 사용자는 `ctf`였다. `USER ctf`가 먹었다.

### exploit으로 죽어도 다음 연결이 사나

산다.
exploit으로 프로그램이 죽은 뒤에도 socat 리스너는 살아 있었다.
`socat ... fork`라 연결마다 새 프로세스가 떠서, 한 번 죽어도 다음 참가자는 새 프로세스를 받는다.

## 짚어둘 것 — exploit에 `ret` 가젯이 하나 필요하다

처음에는 리턴 주소를 `win`으로 바로 덮었는데 flag가 안 나왔다.
`win()`의 `puts("flag:")`까지는 실행됐는데 `system("cat ...")`에서 멈췄다.

**왜 멈췄나?**

리턴 주소 덮어쓰기로 `win`에 들어가면 RSP가 16바이트 정렬이 아니다.
`system()` 내부가 SSE 명령(`movaps`)을 쓰는데, 이 명령은 16바이트 정렬을 요구해서 그대로 죽는다.
`call`로 들어온 게 아니라 `ret`로 들어와서 정렬이 8바이트 어긋나 있기 때문이다.

**어떻게 풀었나?**

`win` 앞에 `ret` 가젯 주소 하나를 끼웠다.
`ret`가 8바이트를 더 소비하면서 정렬이 맞춰진다.

```
payload = b"A"*40 + p64(ret_gadget) + p64(win)
```

이건 문제 바이너리의 결함이 아니라, 정적 링크된 glibc에서 흔한 ret2win 함정이다.
출제자가 참가자에게 주는 설명이나 예시 풀이에 이 점을 적어두면 좋다.
`test-problems/03-pwn-bof/README.md`의 로컬 풀이 예시에도 `ret` 가젯을 넣어 고쳐둘 만하다.

## 정리

| 확인 항목 | 결과 |
| --- | --- |
| S3에서 받은 문제가 등록 원본과 동일 | 통과 |
| 이미지 빌드 (EXPOSE 31337) | 통과 |
| PER_USER 독립 컨테이너 2개 | 통과 |
| 두 컨테이너에서 exploit → flag | 통과 |
| flag 파일 보호 (ctf가 삭제 불가) | 통과 |
| 하드닝 (CapDrop/no-new-privileges/mem/pids/USER) | 통과 |
| socat fork 복구 | 통과 |
| 포트 풀 / `ctf-pwn` 네트워크 / egress / TTL | 미확인 (구현 전) |

## 다음 할 일

1. ADR-0020 작업 1번 — `protocol`에 `AccessType`(HTTP/TCP) 추가, TCP + SHARED reject
2. 포트 풀을 만든 뒤, 이 문제로 밖에서 `nc host port` 접속을 확인
3. `ctf-pwn` 네트워크에서 egress 차단과 컨테이너 간 통신 차단을 확인
4. `03-pwn-bof/README.md` 로컬 풀이 예시에 `ret` 가젯 보정 추가
