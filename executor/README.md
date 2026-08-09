# executor

isolate + 언어 런타임(gcc/g++, python3, openjdk-21)이 세팅된 Executor 이미지.
역할 분리는 [ADR-0005](../docs/adr/%5BADR-0005%5D%20Isolate%20%EA%B8%B0%EB%B0%98%20%ED%94%84%EB%A1%9C%EC%84%B8%EC%8A%A4%20%EC%83%9D%EC%84%B1.md) 참고.

- Orchestrator(Redis 컨슈머, S3, 결과 직렬화 등 비즈니스 로직)는 `worker/`의 별도 이미지에서 돈다.
- 이 이미지는 isolate 호출만 담당하며, Orchestrator와는 `/var/run/judge` 아래 유닉스 소켓으로만 통신한다.

## TODO

- `executor-wrapper` 실행 파일 빌드 후 `Dockerfile`의 `COPY executor-wrapper` 주석 해제.
- 배포 대상 환경(예: Lightsail 인스턴스)에서 `isolate-check-environment` 통과 여부 확인.
  실패 시 ADR-0005 "단점" 항목에 따라 컨테이너를 `--privileged`로 격리하는 폴백 적용.
- `Dockerfile`의 `cg_root = /sys/fs/cgroup` 값이 실제 cgroup v2 delegation과 맞는지 PoC로 검증.
