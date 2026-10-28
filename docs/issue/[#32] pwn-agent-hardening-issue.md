# [#32] pwn 인스턴스 자원 하드닝 (CPU·디스크 상한)

> 선행: agent 배선·포트 풀 완료. ADR-0017(적대적 프로세스), ADR-0020 참고합니다.
> 해당 이슈는 **agent runtime의 컨테이너 자원 상한**을 다룰 예정입니다.
> 문제 컨테이너 전체(web·pwn)에 적용하되, pwn에서 제일 급합니다. gVisor 적용·TTL 회수는 별도 이슈입니다.

### 배경

지금 문제 컨테이너에 걸린 상한은 **메모리와 PID뿐**이다.
`CapDrop: ALL`, `no-new-privileges`도 있다(`runtime/docker.go`).

문제는 **CPU와 디스크에 상한이 없다는 것**이다.
문제 컨테이너는 뚫으라고 만든 적대적 프로세스다(ADR-0017). pwn은 셸을 따는 게 정답이라 더 그렇다.
셸을 딴 참가자가 지금 할 수 있는 것:

* **CPU 고갈.** 무한 루프 몇 개면 호스트 전체가 느려진다. 다른 인스턴스와 조율자·redis까지 영향받는다.
* **디스크 채우기.** `dd if=/dev/zero of=big`로 호스트 디스크를 채우면 redis와 도커가 같이 죽는다.

둘 다 인스턴스 하나로 호스트 전체를 망가뜨리는 길이다. 이걸 막는다.

### 설계

**1. CPU 상한 (`NanoCPUs`)**

컨테이너가 쓸 수 있는 CPU를 제한한다. 코어 수를 나노 단위로 준다(0.5코어 = 500_000_000).

* `runtime.Options`에 `CPULimit float64`를 추가한다(메모리·PID와 같은 자리).
* `hostCfg.NanoCPUs = int64(cpu * 1e9)`.
* `main`에서 `CTF_INSTANCE_CPU`(기본 `0.5`)로 주입한다.

web·pwn 모두에 적용한다. 전부 적대적 컨테이너라 구분할 이유가 없다.

**2. 디스크 쓰기 제한 (read-only rootfs + 크기 제한 tmpfs)**

도커에서 디스크를 직접 제한하는 `--storage-opt size=`는 스토리지 드라이버를 탄다(xfs+pquota 필요). 호스트마다 되고 안 되고가 갈려서 못 쓴다.
대신 **파일시스템을 읽기 전용으로 만들고, 쓸 수 있는 자리만 크기 제한 tmpfs로 연다.** tmpfs는 크기를 못박을 수 있다.

* `hostCfg.ReadonlyRootfs = true`
* `/tmp`에 크기 제한 tmpfs 마운트 (`CTF_INSTANCE_TMPFS_SIZE`, 기본 `64m`)

tmpfs는 메모리를 쓰므로 메모리 상한에도 같이 묶인다. 디스크를 채울 수가 없어진다.

**read-only rootfs가 DYNAMIC 플래그 쓰기와 충돌한다 — 결정 필요**

지금 DYNAMIC은 entrypoint가 env `FLAG`를 파일로 옮긴다(`03-pwn-bof`는 `/home/ctf/flag`에 쓴다).
rootfs가 읽기 전용이면 이 쓰기가 실패한다.

추천: **런타임에 쓰는 파일(DYNAMIC 플래그 포함)은 `/tmp` 아래에 쓴다**로 규칙을 정한다.
`/tmp`만 쓸 수 있는 tmpfs이고, 나머지는 전부 읽기 전용이다.

* STATIC 문제(플래그를 이미지에 구움, `/chall/flag`를 읽기만): 영향 없다. read-only로 잘 돈다.
* DYNAMIC 문제: entrypoint가 `/tmp/flag`에 쓰고 거기서 읽게 바꾼다. 문제 설명·템플릿·`03-pwn-bof`를 같이 고친다.

이 결정이 싫으면 대안은 "쓰기가 필요한 경로마다 tmpfs를 더 마운트"인데, 경로가 문제마다 달라서 설정이 늘어난다. `/tmp` 하나로 모으는 게 단순하다.

**3. 지금 있는 것은 유지**

`CapDrop: ALL`, `no-new-privileges`, 메모리, PID는 그대로 둔다.
도커 기본 seccomp 프로파일도 켜져 있다(명시 안 해도 적용). 끄지 않는다.

### 작업

* [ ] `runtime/docker.go`: `Options`에 `CPULimit`, 그리고 tmpfs 크기 필드 추가
* [ ] `runtime/docker.go`: `Run`에서 `NanoCPUs`, `ReadonlyRootfs=true`, `/tmp` tmpfs 마운트 적용
* [ ] `cmd/agent/main.go`: `CTF_INSTANCE_CPU`(기본 0.5), `CTF_INSTANCE_TMPFS_SIZE`(기본 64m) 주입
* [ ] `03-pwn-bof`: DYNAMIC 플래그를 `/tmp/flag`에 쓰도록 entrypoint 수정, 설명 반영
* [ ] `docs/features/PwnInstance.md`: 출제 규칙에 "런타임 쓰기는 /tmp" 추가
* [ ] 테스트: 자원 상한이 HostConfig에 실제로 들어가는지 (runtime 단위 테스트 또는 컨테이너 테스트)

### 완료 조건

* [ ] 컨테이너 안에서 `:(){ :|:& };:` 류로 CPU를 물려도 호스트가 버틴다 (NanoCPUs 적용)
* [ ] 컨테이너 안에서 `/`에 파일 생성이 거부된다 (read-only rootfs)
* [ ] `/tmp`에는 쓸 수 있지만 `CTF_INSTANCE_TMPFS_SIZE`를 넘기면 거부된다
* [ ] STATIC 문제(`/chall/flag`)가 read-only에서 정상 동작한다
* [ ] DYNAMIC 문제가 `/tmp/flag`로 정상 동작한다
* [ ] `go build ./...`와 `go test ./agent/...`가 통과한다
* [ ] 메모리·PID·CapDrop·no-new-privileges는 그대로다 (회귀 없음)

### 이번에 하지 않는 것

* **gVisor(runsc) 적용.** 커널 격리는 ADR-0008. 이 이슈는 cgroup·마운트 레벨 상한만 건다.
* **TTL 회수(reaper).** 만료된 인스턴스를 내리는 건 ADR-0014 별도 이슈.
* **연결 수 제한.** socat `max-children`, IP당 동시 연결은 출제자 템플릿·게이트웨이 쪽.
* **네트워크 격리.** egress·IMDS·호스트 접근 차단은 RB-0001(인프라)에서 다룬다.
