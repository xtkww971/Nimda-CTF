#!/usr/bin/env bash
# CTF 서버(EC2) 배포 스크립트. CI(.github/workflows/ctf-cd.yml)가 docker-compose.prod.yml과
# 함께 배포 폴더로 보내고 실행한다. 서버에서 손으로 돌려도 된다.
#
#   ./deploy.sh <tag>      지정 태그로 배포. CI는 커밋 short sha를 넘긴다.
#   ./deploy.sh rollback   직전에 성공한 태그로 되돌린다.
#   ./deploy.sh status     현재 태그와 컨테이너 상태.
#
# 웹 백엔드(nimda-web-backend/deploy.sh)와 달리 blue-green을 하지 않는다.
#   * agent·provisioner는 HTTP를 받지 않고 Redis Streams를 읽는 consumer다. 잠깐 내려가도
#     메시지는 스트림에 남고, 다시 뜨면 pending을 XAUTOCLAIM으로 회수한다.
#   * agent를 두 개 띄우면 같은 도커 데몬·포트 풀을 두 프로세스가 만진다. 동시에 둘이
#     살아 있는 구간 자체를 만들면 안 된다.
#   * 문제 컨테이너는 agent와 별개 컨테이너라 agent를 재시작해도 내려가지 않는다.
#     agent는 시작할 때 떠 있는 컨테이너가 쓰는 포트를 빼고 포트 풀을 다시 채운다.
set -euo pipefail

cd "$(dirname "$0")"

COMPOSE_FILE=docker-compose.prod.yml
APP_SERVICES=(agent provisioner dashboard instance-gateway docker-proxy alloy loki prometheus grafana)
MONITORING_CONTAINERS=(ctf-alloy ctf-loki ctf-prometheus ctf-grafana)
STATE_FILE=.deploy_state
LOG_FILE=deployment.log
HEALTH_TIMEOUT=90   # 초. agent는 redis 대기 + 포트 풀 계산 뒤에 ready를 찍는다.
STABLE_SECONDS=15   # ready 뒤에도 이만큼 재시작 없이 살아 있어야 성공으로 본다.

log() { echo "[$(date '+%F %T')] $*" | tee -a "$LOG_FILE"; }
die() { log "❌ $*"; exit 1; }

compose() { docker compose -f "$COMPOSE_FILE" --env-file .env "$@"; }

state_get() { [ -f "$STATE_FILE" ] && grep -E "^$1=" "$STATE_FILE" | cut -d= -f2- || true; }

state_set() {
  printf 'current=%s\nprevious=%s\n' "$1" "$2" > "$STATE_FILE"
}

preflight() {
  [ -f "$COMPOSE_FILE" ] || die "$COMPOSE_FILE 이 없습니다."
  [ -f .env ] || die ".env 가 없습니다. .env.example 을 복사해 채우세요."
  docker compose version >/dev/null 2>&1 || die "docker compose(v2)가 없습니다."

  # 둘 다 옵션·방화벽과 같이 만들어야 해서 여기서 자동 생성하지 않는다.
  docker network inspect ctf-pwn >/dev/null 2>&1 \
    || die "ctf-pwn 네트워크가 없습니다. docs/runbook/[RB-0001] 을 먼저 수행하세요."
  docker network inspect ctf-instances >/dev/null 2>&1 \
    || die "ctf-instances 네트워크가 없습니다. docs/runbook/[RB-0002] 를 먼저 수행하세요."

  # 모니터링 데이터 디렉터리(ADR-0024). 없으면 도커가 root 소유로 만들어 Loki·Prometheus·Grafana가
  # 쓰기 권한 없이 재시작을 반복한다. 소유자까지 맞춰야 해서 여기서 만들지 않는다.
  local mon_dir
  mon_dir=$({ grep -E '^MONITORING_DATA_DIR=' .env || true; } | head -1 | cut -d= -f2-)
  mon_dir=${mon_dir:-/var/lib/monitoring}
  for d in loki prometheus grafana; do
    [ -d "$mon_dir/$d" ] || die "$mon_dir/$d 가 없습니다. docs/runbook/[RB-0004] 단계 2를 먼저 수행하세요."
  done
  # 별도 볼륨이 아니면 모니터링 데이터가 루트 디스크(도커와 같은 디스크)에 쌓인다. 막지는 않는다.
  mountpoint -q "$mon_dir" 2>/dev/null \
    || log "⚠️ $mon_dir 가 별도 볼륨이 아닙니다. 모니터링 데이터가 루트 디스크에 쌓입니다(RB-0004 단계 1)."

  # 문법·필수 변수(:?) 검사. 비밀값이 출력되지 않게 결과는 버린다.
  compose config -q || die "compose 설정 검사 실패 (.env 필수 값 누락 가능)."
}

# 컨테이너가 떠 있고, 재시작 없이, 준비 로그를 찍었는지.
container_ok() {
  local name=$1 ready_pattern=$2
  local status restarts
  status=$(docker inspect -f '{{.State.Status}}' "$name" 2>/dev/null || echo missing)
  restarts=$(docker inspect -f '{{.RestartCount}}' "$name" 2>/dev/null || echo 0)
  [ "$status" = running ] || return 1
  [ "$restarts" = 0 ] || return 1
  if [ -n "$ready_pattern" ]; then
    docker logs "$name" 2>&1 | grep -q "$ready_pattern" || return 1
  fi
}

all_ok() {
  container_ok ctf-agent "agent ready" \
    && container_ok ctf-provisioner "provisioner ready" \
    && container_ok ctf-dashboard "" \
    && container_ok ctf-instance-gateway "" \
    && curl -fsS -o /dev/null --max-time 3 http://127.0.0.1:6070/
}

wait_healthy() {
  local waited=0
  until all_ok; do
    waited=$((waited + 5))
    [ "$waited" -ge "$HEALTH_TIMEOUT" ] && return 1
    sleep 5
  done
  # ready를 찍고 바로 죽는 경우(예: consumer group 생성 실패)를 거른다.
  sleep "$STABLE_SECONDS"
  all_ok
}

dump_logs() {
  for c in ctf-agent ctf-provisioner ctf-dashboard ctf-instance-gateway "${MONITORING_CONTAINERS[@]}"; do
    log "--- $c (최근 30줄)"
    docker logs --tail 30 "$c" 2>&1 | tee -a "$LOG_FILE" || true
  done
}

release() {
  local tag=$1
  export CTF_IMAGE_TAG=$tag

  log "[1/4] 이미지 받기 (tag=$tag)"
  compose pull "${APP_SERVICES[@]}"

  # 상태가 전부 메모리에 있는 통이다. 설정이 바뀌어도 여기서는 다시 만들지 않는다.
  # redis 설정을 바꿨다면 인스턴스 기록이 날아가는 걸 감수하고 손으로 재생성한다.
  log "[2/4] ctf-redis 확인 (재생성하지 않음)"
  compose up -d --no-recreate ctf-redis

  log "[3/4] 앱 컨테이너 교체"
  compose up -d --no-deps "${APP_SERVICES[@]}"

  # 게이트웨이 설정 파일(instance-gateway/nginx.conf)만 바뀌면 compose는 컨테이너를 다시 만들지 않는다.
  # 검사 후 reload해서 새 설정을 읽게 한다. 검사가 실패하면 기존 설정이 그대로 돌고 배포는 실패로 끝난다.
  # 설정은 폴더 mount(/etc/nginx/gw)라 -c로 경로를 같이 넘긴다. 기본 경로(/etc/nginx/nginx.conf)는 이미지 기본값이다.
  local gw_conf=/etc/nginx/gw/nginx.conf
  if docker exec ctf-instance-gateway nginx -t -c "$gw_conf" >/dev/null 2>&1; then
    docker exec ctf-instance-gateway nginx -s reload -c "$gw_conf" >/dev/null
  else
    docker exec ctf-instance-gateway nginx -t -c "$gw_conf" 2>&1 | tee -a "$LOG_FILE" || true
    return 1
  fi

  # alloy·loki·prometheus는 설정 파일을 처음 뜰 때만 읽는다. 파일만 바뀌면 compose가 다시 만들지 않으니
  # 매번 재시작한다. 데이터와 Alloy의 전송 버퍼는 볼륨에 있어 재시작해도 이어진다.
  # Grafana는 대시보드 파일을 30초마다 다시 읽으므로 재시작하지 않는다.
  # 실패해도 CTF와 무관하니 배포를 멈추지 않는다.
  compose restart loki prometheus alloy >/dev/null \
    || log "⚠️ 모니터링 컨테이너 재시작 실패. 새 설정이 반영되지 않았을 수 있습니다."

  log "[4/4] 헬스체크 (최대 ${HEALTH_TIMEOUT}s + 안정화 ${STABLE_SECONDS}s)"
  wait_healthy || return 1

  # 모니터링은 CTF 동작과 무관하다. 죽어 있어도 배포를 실패로 돌리지 않고 경고만 남긴다(ADR-0024).
  local c
  for c in "${MONITORING_CONTAINERS[@]}"; do
    container_ok "$c" "" \
      || log "⚠️ $c 가 정상이 아닙니다. 로그·메트릭 수집이나 대시보드가 멈췄을 수 있습니다: docker logs $c"
  done
}

deploy() {
  local tag=$1
  local current
  current=$(state_get current)

  log "🚀 배포 시작: ${current:-없음} → $tag"
  preflight

  if release "$tag"; then
    # 같은 태그를 다시 배포했으면 previous를 덮지 않는다(롤백 대상이 사라지지 않게).
    if [ "$current" = "$tag" ]; then
      state_set "$tag" "$(state_get previous)"
    else
      state_set "$tag" "$current"
    fi
    # dangling만 지운다. -a를 붙이면 agent가 빌드해 둔 문제 이미지까지 지워져서
    # 다음 인스턴스 요청마다 다시 빌드하게 된다.
    docker image prune -f >/dev/null || true
    log "✅ 배포 완료: $tag"
    return 0
  fi

  log "❌ 헬스체크 실패: $tag"
  dump_logs
  if [ -n "$current" ] && [ "$current" != "$tag" ]; then
    log "⏮️ 직전 태그로 자동 복구: $current"
    if release "$current"; then
      log "✅ 복구 완료: $current 유지"
    else
      dump_logs
      log "🚨 복구도 실패했습니다. 서버에서 직접 확인이 필요합니다."
    fi
  fi
  exit 1
}

rollback() {
  local current previous
  current=$(state_get current)
  previous=$(state_get previous)
  [ -n "$previous" ] || die "되돌릴 이전 태그가 없습니다."
  log "⏮️ 롤백: $current → $previous"
  preflight
  release "$previous" || { dump_logs; die "롤백 헬스체크 실패: $previous"; }
  state_set "$previous" "$current"
  log "✅ 롤백 완료: $previous"
}

status() {
  echo "current : $(state_get current)"
  echo "previous: $(state_get previous)"
  compose ps
}

case "${1:-}" in
  rollback) rollback ;;
  status)   status ;;
  ""|-h|--help)
    echo "usage: $0 <tag> | rollback | status"; exit 1 ;;
  *)        deploy "$1" ;;
esac
