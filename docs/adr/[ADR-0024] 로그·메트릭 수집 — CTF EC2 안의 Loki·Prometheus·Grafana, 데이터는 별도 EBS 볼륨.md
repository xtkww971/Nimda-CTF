# [ADR-0024] 로그·메트릭 수집 — CTF EC2 안의 Loki·Prometheus·Grafana, 데이터는 별도 EBS 볼륨

* **날짜:** 2026-11-23
* **작성자:** MaTuna01
* **상태:** 제안

## 1. Context (배경)

CTF 서버에서 무슨 일이 있었는지 보려면 EC2에 SSH로 들어가 `docker logs`를 컨테이너마다 쳐야 한다.
배포하면서 컨테이너를 새로 만들면 그 전 로그는 사라진다. 대회 중에 "아까 인스턴스가 왜 안 떴나"를 되짚을 길이 없다.
CPU·메모리·큐 적체도 보는 곳이 없다. Feature-01 대시보드는 지금 떠 있는 인스턴스 목록만 보여준다.

이미 갖춘 것도 있다. agent·provisioner·dashboard는 `slog.NewJSONHandler(os.Stdout)`로 JSON 한 줄씩 찍는다.
수집기가 파싱 없이 받을 수 있는 형태다.

제약은 이렇다.

* **CTF EC2는 대회와 테스트 때만 켜고 나머지는 꺼 둔다.** 그래도 수집한 로그·메트릭은 남겨서 나중에 다시 보고 싶다.
* CTF EC2에는 참가자가 다루는 **적대적인 문제 컨테이너**가 돈다(ADR-0017).
* docker.sock을 쥐는 프로세스는 호스트 root와 같다. 그래서 agent만 소켓을 쥐도록 나눴다(ADR-0009).

## 2. Decision (결정)

**CTF EC2 안에 수집(Alloy)·저장(Loki·Prometheus)·화면(Grafana)을 같이 띄운다.
저장 데이터는 루트 디스크가 아니라 별도 EBS 볼륨에 두고, 보관 기간을 길게 잡는다.**

```
[CTF EC2 · 대회·테스트 때만 켬]
 agent / provisioner / gateway / 문제 컨테이너
        │ stdout
 docker-proxy (GET만) ── Alloy ──push──▶ Loki ─────┐
 ctf-redis ─────────────────┘      └──▶ Prometheus ┴─▶ Grafana (127.0.0.1:3000, SSH 터널)
                                            │
                              /var/lib/monitoring  ← 별도 EBS 볼륨 (EC2를 꺼도 남는다)
```

| 항목 | 값 |
|---|---|
| 로그 저장 | Loki 3.5 단일 바이너리, 파일시스템, 180일(`LOKI_RETENTION_PERIOD`) |
| 메트릭 저장 | Prometheus 3.5, 180일 또는 10GB 중 먼저 닿는 쪽(`PROMETHEUS_RETENTION`, `_SIZE`) |
| 시각화 | Grafana 12, 데이터소스·대시보드는 파일로 프로비저닝(`deploy/monitoring/grafana`) |
| 수집기 | Grafana Alloy 1.10 (`deploy/alloy/config.alloy`) |
| 데이터 위치 | 별도 EBS 볼륨 `MONITORING_DATA_DIR`(기본 `/var/lib/monitoring`)의 `loki/`, `prometheus/`, `grafana/` |
| 로그 대상 | 이 호스트의 모든 컨테이너 stdout. 라벨: `host`, `container`, `service`, `kind`(system/challenge), `level` |
| 메트릭 대상 | 호스트(node exporter), 컨테이너(cAdvisor), ctf-redis(메모리, 스트림 길이·pending·lag) |
| 배포 | 다른 서비스와 같은 `docker-compose.prod.yml`, 같은 CI·`deploy.sh` |

### 왜 ELK가 아니라 Loki인가?

Elasticsearch 하나만 해도 메모리 2~4GB가 든다. Loki는 본문을 색인하지 않고 라벨만 색인해서 수백 MB로 돈다.
이 규모(동아리 CTF, 컨테이너 수십~수백 개)에서는 전문 검색이 필요 없다. LogQL의 `|=`, `| json`으로 충분하다.
메트릭(Prometheus)과 한 화면(Grafana)에서 같이 볼 수 있다는 점도 크다.

### 왜 같은 EC2인가?

처음에는 같은 VPC에 모니터링 전용 EC2를 따로 두려고 했다. 서버가 죽을 때 모니터링이 같이 죽지 않고,
문제 컨테이너와 자원을 다투지 않기 때문이다. 다른 선택지와 비교하면 이렇다.

| 안 | 장점 | 단점 |
|---|---|---|
| 별도 EC2 (상시 켬) | 장애와 분리, 자원 분리 | CTF가 꺼져 있는 대부분의 시간에도 비용이 든다. 그동안 볼 것도 없다 |
| 별도 EC2 (같이 켜고 끔) | 장애와 분리 | 켜고 끌 대상이 둘이 된다. 하나만 켜면 수집이 끊긴다 |
| Lightsail 웹 서버 | 비용 없음 | 웹이 뚫리면 로그가 같이 샌다. JVM과 메모리를 다툰다 |
| **CTF EC2 안** | 비용 없음. 모니터링이 필요한 시간이 곧 CTF가 켜진 시간이다. 네트워크(보안 그룹·사설 IP)가 필요 없다 | 장애·자원·침입을 CTF와 같이 겪는다(3절) |

CTF EC2를 대회·테스트 때만 켠다면, 모니터링이 필요한 시간과 CTF가 켜져 있는 시간이 같다.
그래서 같은 EC2에 두고, 단점은 별도 볼륨과 자원 상한으로 줄인다. 나중에 옮겨야 하면 Alloy의 push 주소만 바꾸면 된다.

### 왜 별도 EBS 볼륨인가?

* **디스크 격리.** 모니터링 데이터가 차도 루트 디스크(도커 이미지·컨테이너)는 멀쩡하다. 루트가 차면 agent의 빌드가 실패해 CTF가 멈춘다.
* **보존.** EC2를 중지해도 EBS는 남는다. EC2를 새로 만들어야 해도 볼륨을 떼어 붙이면 된다. 스냅샷으로 백업할 수 있다.

### 보관 기간을 길게 잡는 이유

Loki와 Prometheus의 보관 기간은 **실제 시각 기준**이다. EC2를 꺼 둔 동안에도 흘러가서, 14일로 두면 한 달 뒤에 켜는 순간
이전 대회 기록이 전부 지워진다. 켜져 있는 날만 쌓이니 180일로 잡아도 실제 양은 작다.
Prometheus는 크기 상한(10GB)도 건다. Loki는 크기 기준 삭제가 없어서, 대시보드 개요의 "모니터링 볼륨"으로 사용량을 본다.

### 왜 push인가? 왜 앱이 Loki로 직접 보내지 않나?

앱은 stdout에 쓰기만 하고 저장소를 모른다. Loki가 죽어도 앱이 느려지지 않고, 나중에 저장소를 바꾸거나 옮겨도 앱을 다시 배포하지 않는다.
Alloy가 모아서 보내고, 저장소가 잠시 죽어도 버퍼(WAL)에 쥐고 있다가 다시 보낸다.

### docker.sock을 Alloy에 주지 않는다

컨테이너 이름·라벨을 알려면 Docker API가 필요하다. 소켓을 직접 주면 Alloy가 agent와 같은 권한을 갖는다(ADR-0009 위반).
`docker-socket-proxy`를 두고 **GET만**(containers, networks, info) 연다. 생성·exec·삭제(POST)는 막힌다.
프록시는 포트를 열지 않고 `ctf-observe` 망에서 Alloy만 붙는다.

같은 이유로 **containerd.sock도 주지 않는다**. cAdvisor의 docker 연동은 도커가 containerd 이미지 저장소를 쓰면
(Docker 29부터 새로 설치하면 기본값) containerd.sock을 요구한다. 그래서 cAdvisor는 docker 연동을 끄고 도커 cgroup을
직접 읽는다. 그러면 컨테이너 이름이 없으므로, Alloy가 Docker API(프록시)에서 받은 목록으로
`ctf_container_info{container_id, container, kind, instance_uuid}` 대응표를 만들고 대시보드가 `container_id`로 조인한다.
도커 저장소 종류와 상관없이 똑같이 동작하고, `/var/lib/docker` 마운트도 필요 없다.

### 망 분리

| 망 | 붙는 것 | 성격 |
|---|---|---|
| `ctf-observe` | docker-proxy, alloy | 다른 컨테이너는 Docker API에 닿지 못한다 |
| `ctf-monitoring` | alloy, loki, prometheus, grafana | `internal`. 밖으로 못 나가고 문제 컨테이너는 닿지 못한다 |
| `ctf-grafana` | grafana | 포트 공개 전용 bridge(127.0.0.1:3000) |

Loki·Prometheus는 호스트에 포트를 열지 않는다. 인증이 없어서 망 밖에서 닿지 못하게 하는 것 자체가 접근 통제다.

### 문제 컨테이너 로그는 적대적인 입력이다

참가자는 문제 컨테이너의 stdout을 마음대로 쓸 수 있다. 무한 출력으로 Loki를 채우는 게 가장 쉬운 공격이다.

* Alloy: `kind="challenge"` 스트림은 컨테이너마다 초당 20줄(버스트 200)까지만 보내고 나머지는 버린다.
* Loki: 스트림당 512KB/s, 줄당 64KB(넘으면 자름), 전체 4MB/s.
* Alloy는 읽은 뒤에 버리므로 읽는 CPU는 든다. `cpus: 0.5`로 묶어 문제 컨테이너와 CPU를 다투지 않게 한다.

### 로그에 남기면 안 되는 것

Grafana 계정이 새거나 데이터 볼륨(또는 스냅샷)이 유출되면 로그가 통째로 보인다. 다음은 어떤 레벨로도 로그에 남기지 않는다.

* 플래그 값. 특히 DYNAMIC 플래그(ADR-0021)는 환경변수로 들어가므로 컨테이너 env를 통째로 찍지 않는다.
* AWS 키, redis 비밀번호, presigned URL 전체(서명이 들어 있다).

참가자를 가리키는 값도 CTF 서버에는 없다(요청에 userId가 없다). 사용자 단위 분석은 웹 로그와 uuid로 조인해서 한다.

## 3. Consequences (결과)

🟢 **장점**

* 배포로 컨테이너가 바뀌어도, EC2를 껐다 켜도 로그·메트릭이 180일 남는다. SSH 터널 하나로 Grafana에서 서비스·레벨·검색어로 거른다.
* 호스트·컨테이너·ctf-redis(큐 적체 포함)를 한 대시보드에서 본다.
* 추가 인스턴스 비용이 없다(EBS 볼륨만, 30GB 기준 월 몇 달러).
* Go 코드를 바꾸지 않았다. 이벤트 로그를 추가하면 그대로 수집된다.
* 모니터링이 죽어도 CTF는 돈다. deploy.sh는 모니터링 상태로 배포 성패를 가르지 않는다.

🔴 **단점·개선안**

* **장애를 같이 겪는다.** EC2가 메모리 부족으로 흔들리거나 죽으면 모니터링도 같이 흔들려서, 직전 기록이 끊길 수 있다.
  정작 원인을 봐야 할 때 못 볼 수 있다. 대회 규모가 커지면 별도 인스턴스로 옮긴다(Alloy push 주소만 바꾸면 된다).
* **자원을 나눠 쓴다.** 모니터링 4개 컨테이너가 메모리 약 1.2GB(상한 기준: Loki 384m, Prometheus 384m, Grafana 192m, Alloy 256m)를 쓴다.
  문제 컨테이너(256m) 5개분이다. EC2 타입을 정할 때 이만큼 더한다.
* **침입 흔적을 지킬 수 없다.** 문제 컨테이너가 탈출해 호스트를 장악하면 로그를 지우거나 조작할 수 있다.
  (장악되면 `.env`의 AWS 키도 이미 보이므로 노출 자체가 크게 늘지는 않는다. 잃는 건 조사용 기록이다.)
* **꺼져 있을 때는 볼 수 없다.** 지난 기록을 보려면 EC2를 켜야 하고, 켜면 CTF 컨테이너도 같이 뜬다.
* Loki·Prometheus는 인증이 없다. 망 분리가 유일한 통제다.
* Alloy는 호스트 `/`, `/proc`, `/sys`를 읽기 전용으로 마운트한다(node exporter). Alloy가 장악되면 호스트 파일을 읽을 수 있다.
* 컨테이너 메트릭에 네트워크 송수신이 없다. cgroup 직접 읽기로는 컨테이너 네트워크 네임스페이스를 못 본다.
* 문제 컨테이너는 compose 밖이라 compose의 로그 로테이션을 받지 않는다. 지금은 daemon.json으로 건다(RB-0004).
  agent가 컨테이너를 만들 때 `LogConfig`를 직접 넣는 게 맞다(2단계).

### 다음 단계

* agent·provisioner에 업무 이벤트 로그 추가(`event=instance.created` 같은 공통 필드, 위의 금지 항목 준수).
* agent·provisioner에 `/metrics`(요청 처리 시간, 빌드 시간, 실패 수) 추가.
* instance-gateway 접근 로그를 JSON으로.
* 웹(Lightsail) 로그도 같은 Loki로 보내 uuid로 조인.
* Grafana 알림(디스크, 모니터링 볼륨, ctf-redis 메모리, 큐 lag, ERROR 급증).

## 4. 참고자료

* ADR-0009 조율자·에이전트 배포 구조, ADR-0017 문제 컨테이너 격리 경계, ADR-0021 DYNAMIC 플래그 주입
* RB-0004 모니터링 서버 구성
* Grafana Alloy `loki.source.docker`, `prometheus.exporter.cadvisor`, `prometheus.exporter.redis` 문서
