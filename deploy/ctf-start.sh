#!/usr/bin/env bash
# EC2를 켠 뒤 서버 안에서 돌린다. CTF 서비스가 다 살아났는지 확인하고, 필요하면 매니페스트대로 복구한다.
#
#   ~/ctf/ctf-start.sh
#
# EC2는 콘솔에서 직접 켠다. 이 스크립트는 AWS를 조작하지 않는다(자격증명 불필요).
#
# 껐다 켜면:
#   * 컨테이너 10개는 restart: unless-stopped라 도커가 뜨면 알아서 다시 뜬다. 보통은 확인만 하고 끝난다.
#   * 공인 IP는 Elastic IP가 아니면 바뀐다. 그러면 pwn 안내 주소(CTF_PUBLIC_HOST)를 고치고 같은 태그로 다시 배포한다.
#   * ctf-redis는 비어서 뜬다(영속화 꺼짐). 끄기 전 인스턴스 기록은 사라진다.
#
# 컨테이너를 직접 띄우지 않는다. 복구가 필요하면 deploy.sh로 현재 태그를 다시 배포한다(AGENTS.md: 서버 상태는 매니페스트로만).
set -euo pipefail

cd "$(dirname "$0")"

EXPECTED="ctf-agent ctf-alloy ctf-dashboard ctf-docker-proxy ctf-grafana ctf-instance-gateway ctf-loki ctf-prometheus ctf-provisioner ctf-redis"

say()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✔ %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m⚠ %s\033[0m\n' "$*"; }
die()  { printf '\033[1;31m✖ %s\033[0m\n' "$*"; exit 1; }

[ -f .env ] || die ".env가 없습니다. 배포 폴더(~/ctf)에서 실행하세요."
[ -f .deploy_state ] || die ".deploy_state가 없습니다. 한 번도 배포되지 않은 서버입니다(CI로 먼저 배포)."
TAG=$(grep '^current=' .deploy_state | cut -d= -f2)
[ -n "$TAG" ] || die ".deploy_state에 current 태그가 없습니다."

# .env에서 값 하나 읽기. 키가 없어도 스크립트가 죽지 않게 빈 값을 돌려준다.
env_get() { { grep -E "^$1=" .env || true; } | head -1 | cut -d= -f2- | tr -d ' '; }
GATEWAY_URL="http://$(env_get CTF_GATEWAY_BIND):8088/"

# 하나도 안 떠 있으면 grep이 1로 끝난다. pipefail 때문에 그대로 두면 복구 단계 전에 스크립트가 죽는다.
running_set() {
  { docker ps --format '{{.Names}}' 2>/dev/null \
      | grep -E '^ctf-(agent|alloy|dashboard|docker-proxy|grafana|instance-gateway|loki|prometheus|provisioner|redis)$' || true; } \
    | sort | tr '\n' ' ' | sed 's/ $//'
}

# ---------------------------------------------------------------- 1. 도커
say "도커 대기 중..."
for i in $(seq 1 30); do
  docker info >/dev/null 2>&1 && break
  [ "$i" = 30 ] && die "60초 동안 도커가 뜨지 않았습니다. sudo systemctl status docker 를 확인하세요."
  sleep 2
done
ok "도커 실행 중"

# ---------------------------------------------------------------- 2. 공인 IP
# IMDSv2. 호스트에서 부르는 거라 hop limit 1이어도 닿는다(컨테이너에서는 막혀 있다, RB-0001).
TOKEN=$(curl -s -X PUT --max-time 3 http://169.254.169.254/latest/api/token \
  -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' || true)
PUBLIC_IP=$(curl -s --max-time 3 -H "X-aws-ec2-metadata-token: $TOKEN" \
  http://169.254.169.254/latest/meta-data/public-ipv4 || true)
ENV_HOST=$(env_get CTF_PUBLIC_HOST)

IP_CHANGED=""
if ! [[ "$PUBLIC_IP" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  warn "공인 IP를 메타데이터에서 못 읽었습니다. CTF_PUBLIC_HOST($ENV_HOST)를 그대로 둡니다."
elif [ "$PUBLIC_IP" != "$ENV_HOST" ]; then
  IP_CHANGED=1
  warn "공인 IP가 바뀌었습니다: ${ENV_HOST:-비어 있음} → $PUBLIC_IP"
  cp -p .env ".env.bak-$(date +%Y%m%d%H%M)"
  sed -i "s/^CTF_PUBLIC_HOST=.*/CTF_PUBLIC_HOST=$PUBLIC_IP/" .env
  ok ".env의 CTF_PUBLIC_HOST를 $PUBLIC_IP 로 바꿨습니다 (백업: .env.bak-*)"
else
  ok "공인 IP 그대로: $PUBLIC_IP"
fi

# ---------------------------------------------------------------- 3. 컨테이너
say "CTF 컨테이너 대기 중... (최대 90초)"
for i in $(seq 1 18); do
  [ "$(running_set)" = "$EXPECTED" ] && break
  sleep 5
done
NOW="$(running_set)"

if [ -n "$IP_CHANGED" ] || [ "$NOW" != "$EXPECTED" ]; then
  if [ -n "$IP_CHANGED" ]; then
    say "새 주소를 반영하려고 현재 태그($TAG)를 다시 배포합니다."
  else
    warn "다 뜨지 않았습니다. 떠 있는 것: ${NOW:-없음}"
    say "매니페스트대로 복구하려고 현재 태그($TAG)를 다시 배포합니다."
  fi
  ./deploy.sh "$TAG" || die "deploy.sh가 실패했습니다. deployment.log를 확인하세요."
else
  ok "컨테이너 10개 모두 실행 중"
fi

# ---------------------------------------------------------------- 4. 점검
say "상태 점검"
FAILS=0
pass() { printf '  \033[32mOK\033[0m   %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; FAILS=$((FAILS + 1)); }

for c in ctf-redis ctf-agent ctf-provisioner ctf-dashboard ctf-instance-gateway ctf-docker-proxy ctf-alloy ctf-loki ctf-prometheus ctf-grafana; do
  s=$(docker inspect -f '{{.State.Status}}' "$c" 2>/dev/null || echo missing)
  if [ "$s" = running ]; then pass "$c"; else fail "$c ($s)"; fi
done

# 모니터링 데이터 볼륨(ADR-0024, RB-0004). fstab의 nofail 때문에 볼륨이 안 붙어도 부팅은 된다.
# 그 상태로 두면 데이터가 루트 디스크의 빈 디렉터리에 쌓이고 지난 기록이 안 보인다.
MON_DIR=$(env_get MONITORING_DATA_DIR); MON_DIR=${MON_DIR:-/var/lib/monitoring}
if mountpoint -q "$MON_DIR"; then
  pass "모니터링 볼륨 $MON_DIR ($(df -h --output=pcent "$MON_DIR" | tail -1 | tr -d ' ') 사용)"
else
  fail "모니터링 볼륨이 $MON_DIR 에 마운트되지 않음 (RB-0004 단계 1)"
fi

if docker network inspect ctf-pwn >/dev/null 2>&1 && docker network inspect ctf-instances >/dev/null 2>&1; then
  pass "망 ctf-pwn, ctf-instances"
else
  fail "망이 없음 (RB-0001, RB-0002)"
fi

PWN_SUBNET=$(docker network inspect ctf-pwn -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' 2>/dev/null || true)
n=$(sudo -n iptables -S DOCKER-USER 2>/dev/null | grep -c -- "-s $PWN_SUBNET" || true)
if [ -n "$PWN_SUBNET" ] && [ "${n:-0}" -ge 3 ]; then
  pass "pwn 격리 규칙 ($PWN_SUBNET, DOCKER-USER ${n}줄)"
else
  fail "pwn 격리 규칙이 ${n:-0}줄 (RB-0001 단계 3, 7)"
fi

code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$GATEWAY_URL" || true)
if [ "$code" = 400 ]; then pass "게이트웨이 $GATEWAY_URL (400 = 정상)"; else fail "게이트웨이 응답 ${code:-없음}"; fi

# heartbeat는 agent snapshot이 생긴 뒤 다음 주기(10초)에 써진다. 조금 기다린다.
PW=$(grep '^WEB_REDIS_PASSWORD=' .env | cut -d= -f2-)
ADDR=$(grep '^WEB_REDIS_ADDR=' .env | cut -d= -f2-)
hb=""
for i in 1 2 3 4 5 6; do
  hb=$(docker run --rm -e REDISCLI_AUTH="$PW" redis:7-alpine \
    redis-cli -h "${ADDR%:*}" -p "${ADDR##*:}" GET ctf:server:heartbeat 2>/dev/null || true)
  [ -n "$hb" ] && break
  sleep 5
done
if [ -n "$hb" ]; then
  pass "웹 heartbeat ($hb): 웹에 [인스턴스 시작] 버튼이 보인다"
else
  fail "웹 heartbeat 없음 (docker logs ctf-provisioner 확인)"
fi

echo "  배포 태그: $(grep '^current=' .deploy_state | cut -d= -f2)"
echo "  pwn 주소: $(grep -E '^CTF_PUBLIC_HOST=' .env | cut -d= -f2-)"

# ---------------------------------------------------------------- 5. 끝
echo
if [ "$FAILS" -eq 0 ]; then ok "CTF 서버 준비 완료"; else warn "점검 실패 ${FAILS}건. 위 FAIL 항목을 확인하세요."; fi
echo "   참고: 껐다 켜면 ctf-redis가 비어서, 끄기 전 인스턴스는 전부 사라진 것으로 봐야 한다."
if [ -n "$IP_CHANGED" ]; then
  echo
  warn "GitHub 시크릿 CTF_SERVER_HOST를 $PUBLIC_IP 로 바꿔야 다음 main 배포가 됩니다."
  echo "   레포 Settings → Secrets and variables → Actions → CTF_SERVER_HOST"
  echo "   로컬 SSH 접속 주소도 $PUBLIC_IP 로 바뀝니다."
fi
[ "$FAILS" -eq 0 ]
