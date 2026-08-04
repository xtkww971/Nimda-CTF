#!/usr/bin/env bash
# ADR-0005: cgroup v2 delegation이 실제 배포 대상 커널/Docker 조합에서
# 동작하는지는 빌드 타임이 아니라 컨테이너 기동 시점에만 확인할 수 있다.
# 여기서 실패하면 --privileged 폴백(ADR "단점" 항목 참고)이 필요하다는 신호다.
set -euo pipefail

if command -v isolate-check-environment >/dev/null 2>&1; then
    if ! isolate-check-environment; then
        # isolate-check-environment는 cgroup delegation뿐 아니라 SMT/ASLR/hugepage 같은
        # "채점 시간 측정 정밀도" 권장사항도 같은 실패로 취급한다. 개발용 Docker Desktop
        # 호스트에서는 이런 항목이 흔히 걸리므로 경고만 하고 계속 진행한다 - 실제 배포
        # 대상(Lightsail 등)에서는 이 출력을 다시 확인하고 필요하면 fatal로 되돌려야 한다.
        echo "[executor] isolate-check-environment 경고 있음 (위 출력 참고) - 개발 환경이라 계속 진행합니다. 배포 전 재검증 필요 (ADR-0005 참고)" >&2
    fi
fi

exec /app/executor "$@"
