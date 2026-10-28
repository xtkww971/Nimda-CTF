# [RB-0001] pwn 네트워크 격리 구성

* **최초 작성일:** 2026-10-28
* **최종 수정일:** 2026-10-28
* **작성자:** 이도현
* **대상 시스템:** Infra (CTF 서버 호스트, AWS)

---

## 1. 개요 및 트리거

pwn(TCP) 문제 인스턴스를 처음 켜기 전에 **한 번** 실행한다.
서버를 새로 구축하거나 호스트를 옮길 때도 다시 한다.

이 절차가 하는 일은 하나다.
**셸을 딴 참가자가 컨테이너 밖으로 못 나가게, 우리 안으로도 못 들어오게 막는다.**
배경 개념은 `Desktop/work/CTF/학습-pwn네트워크격리.md`, 설계는 ADR-0020에 있다.

이 절차를 안 하면 두 가지가 깨진다.

* `ctf-pwn` 네트워크가 없어서 pwn 컨테이너 생성이 "network ctf-pwn not found"로 실패한다.
* 네트워크만 만들고 방화벽을 안 걸면, 셸을 딴 참가자가 인터넷으로 나가거나(reverse shell) 호스트의 ctf-redis에 붙을 수 있다.

## 2. 사전 준비 사항 및 리스크

* **필요 권한:** CTF 서버 SSH 접근 + `sudo`. AWS 보안 그룹과 EC2 설정 변경 권한.
* **주의사항:**

  > ⚠️ iptables 규칙을 잘못 넣으면 다른 컨테이너 통신까지 끊길 수 있다.
  > 규칙은 `-I`로 넣고, 잘못 넣었으면 같은 규칙을 `-D`로 지운다(6절).
  > 넣기 전에 `sudo iptables -L DOCKER-USER -n --line-numbers`로 현재 상태를 적어둔다.

  > ⚠️ 서브넷(`172.x.0.0/16`)은 `ctf-pwn`을 만든 뒤 확인한 값으로 바꿔 쓴다.
  > 아래 명령의 `<PWN_SUBNET>`을 그 값으로 치환한다. 네트워크를 다시 만들면 대역이 바뀔 수 있다.

## 3. 단계별 실행 절차

### 단계 1. ctf-pwn 네트워크 생성

컨테이너끼리 통신(`enable_icc`)을 끈 bridge 네트워크를 만든다.
이름은 agent가 쓰는 `CTF_PWN_NETWORK`(기본 `ctf-pwn`)와 같아야 한다.

```bash
docker network create \
  --driver bridge \
  --opt com.docker.network.bridge.enable_icc=false \
  ctf-pwn
```

이미 있으면 "already exists"가 난다. 그대로 두면 된다.

### 단계 2. 서브넷 확인

방화벽 규칙에 쓸 대역을 읽는다. 이 값을 아래에서 `<PWN_SUBNET>`으로 쓴다.

```bash
docker network inspect ctf-pwn -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}'
# 예: 172.20.0.0/16
```

### 단계 3. 나가는 연결 차단 (egress)

`ctf-pwn`에서 **새로** 나가는 연결을 버린다. 참가자가 들어온 연결의 응답(established)은 살린다.
DOCKER-USER 체인은 컨테이너가 호스트를 거쳐 나가는(FORWARD) 경로다.

```bash
# 들어온 연결의 응답은 허용 (이 규칙이 DROP보다 위에 와야 한다)
sudo iptables -I DOCKER-USER -s <PWN_SUBNET> -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
# 클라우드 메타데이터(IMDS)로 가는 건 먼저 끊는다 (단계 5와 짝)
sudo iptables -I DOCKER-USER -s <PWN_SUBNET> -d 169.254.169.254 -j DROP
# 그 외 새로 나가는 연결은 전부 버린다
sudo iptables -A DOCKER-USER -s <PWN_SUBNET> -m conntrack --ctstate NEW -j DROP
```

> `-I`(앞에 끼움)와 `-A`(뒤에 붙임)를 섞어 쓴 이유는 순서 때문이다.
> established 허용과 IMDS 차단은 위에, NEW 차단은 아래에 와야 한다.
> 넣은 뒤 `sudo iptables -L DOCKER-USER -n --line-numbers`로 순서를 확인한다.

### 단계 4. 호스트 내부 서비스 접근 차단

컨테이너가 호스트 자신(gateway IP)에 붙는 건 FORWARD가 아니라 INPUT이라, DOCKER-USER로는 못 막는다.
셸을 딴 참가자가 gateway로 ctf-redis에 붙어 포트 풀을 조작하는 걸 여기서 막는다.

```bash
sudo iptables -I INPUT -s <PWN_SUBNET> -j DROP
```

ctf-redis가 호스트 포트로 열려 있지 않은지도 확인한다. compose에서 ctf-redis는 `ctf-internal`에만 있고 `ports:` 매핑이 없어야 한다.

```bash
docker inspect ctf-redis -f '{{json .HostConfig.PortBindings}}'   # {} 여야 한다
```

### 단계 5. EC2 메타데이터(IMDS) 잠그기

단계 3에서 방화벽으로 막았고, EC2 자체도 IMDSv2 전용 + hop limit 1로 바꾼다.
hop이 하나 더 붙는 컨테이너 요청은 이걸로도 막힌다.

```bash
aws ec2 modify-instance-metadata-options \
  --instance-id <INSTANCE_ID> \
  --http-tokens required \
  --http-put-response-hop-limit 1
```

### 단계 6. 보안 그룹 열기

pwn 포트 풀 범위를 인바운드에 연다. 범위는 agent의 `CTF_PWN_PORT_RANGE`(기본 `30000-30199`)와 같아야 한다.

AWS 콘솔 또는 CLI로 해당 인스턴스 보안 그룹에 추가한다.

```
Type: Custom TCP | Port range: 30000-30199 | Source: 0.0.0.0/0
```

### 단계 7. 방화벽 규칙 영속화

iptables 규칙은 재부팅하면 사라진다. 저장해서 남긴다.

```bash
sudo apt-get install -y iptables-persistent     # 처음 한 번
sudo netfilter-persistent save
```

## 4. 검증

아래가 전부 기대대로 나오면 끝이다.
확인용 컨테이너를 `ctf-pwn`에 띄워서 본다.

```bash
docker run --rm -it --network ctf-pwn alpine sh
```

컨테이너 안에서:

```sh
wget -qO- --timeout=3 https://example.com      # (egress) 실패해야 한다
wget -qO- --timeout=3 http://169.254.169.254/  # (IMDS)   실패해야 한다
ip route | grep default                         # gateway IP 확인
nc -w3 <GATEWAY_IP> 6379                         # (호스트) 붙으면 안 된다
```

컨테이너 둘을 띄워 서로 IP로 ping이 안 되면 `enable_icc=false`가 먹은 것이다.
참가자가 들어온 연결로 `03-pwn-bof`를 정상적으로 푸는지도 확인한다(established 허용).

## 5. 완료 조건

- [ ] `ctf-pwn` 네트워크가 있고 `enable_icc=false`다
- [ ] 컨테이너에서 인터넷으로 못 나간다
- [ ] 컨테이너에서 `169.254.169.254`에 못 붙는다
- [ ] 컨테이너에서 호스트 gateway의 ctf-redis에 못 붙는다
- [ ] 같은 네트워크 컨테이너끼리 못 붙는다
- [ ] 참가자는 `nc`로 정상 접속해 문제를 푼다
- [ ] 보안 그룹에 `30000-30199/tcp`가 열려 있다
- [ ] 재부팅 후에도 규칙이 남는다

## 6. 원복 (규칙 제거)

연습했거나 잘못 넣었을 때 되돌린다. `-I`/`-A`로 넣은 규칙을 `-D`로 지운다.

```bash
sudo iptables -D DOCKER-USER -s <PWN_SUBNET> -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
sudo iptables -D DOCKER-USER -s <PWN_SUBNET> -d 169.254.169.254 -j DROP
sudo iptables -D DOCKER-USER -s <PWN_SUBNET> -m conntrack --ctstate NEW -j DROP
sudo iptables -D INPUT -s <PWN_SUBNET> -j DROP
sudo netfilter-persistent save
```

네트워크까지 지우려면(쓰는 컨테이너가 없을 때):

```bash
docker network rm ctf-pwn
```
