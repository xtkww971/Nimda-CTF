# ADR: 채점 서버 샌드박싱 아키텍처 — Pre-warmed Docker Worker Pool + Isolate

## 1. Context

 매 채점 요청마다 새로운 격리 프로세스/환경을 생성하면 지연 시간과 CPU 사용량 오버헤드가 트래픽 몰릴 때 병목이 됨.
 따라서 도커 컨테이너를 미리 N개 생성해서 Isolate 생성 오버헤드를 줄이는 방식을 사용해야 함

## 2. Decision

채점용 격리 환경(Isolate)을 미리 띄워둔 Docker 워커 풀 내부에서 실행하되, **워커 컨테이너 내부의 권한도 한 번 더 분리**하는 구조를 채택.
여기서 말하는 내부의 권한은 문제 조건에 있는 메모리 사용량, 시간 제한 등을 의미한다.

### 2.1 기본 구조: Pre-warmed Worker Pool

컴파일러들과 isolate 바이너리가 세팅된 Docker 이미지를 미리 빌드해두고, 채점 서버는 이 이미지로 워커 컨테이너를 N개 상시 기동 상태로 유지한다. 요청마다 컨테이너를 새로 띄우는 대신, 이미 떠 있는 컨테이너 내부에서 isolate box만 초기화/정리해서 재사용한다.

### 2.2 워커 내부 권한 분리 (Orchestrator / Executor)

워커 컨테이너 안의 프로세스를 역할별로 나눈다.

- **Executor** (최소 기능 및 권한): isolate 호출만 담당하는 아주 작은 프로세스. cgroup 제어에 필요한 최소 권한만 보유하고
Orchestrator와는 로컬 유닉스 소켓으로만 통신. exec 인자 배열로 isolate를 호출해 인젝션 여지를 없앤다.

- **Orchestrator** (비특권): Redis 컨슈머, S3 테스트케이스 load, 결과 비교/직렬화, Redis stream publish 등 
모든 비즈니스 로직을 전담. Executor에게 코드와 문제 제한 사항을 통해 요청만 보내고 stdout·exit code·시간·메모리만 돌려받는다.

- **Watcher** (Isolate 컨테이너 생성 권한): Redis 큐의 길이를 보고 컨테이너의 수를 조절하는 프로세스


### 🟢 장점

- **프로세스 생성 오버헤드 감소**: 컨테이너를 상시 대기 상태로 유지하고 isolate box만 초기화하므로 환경 구축 비용이 대폭 절감됨.
- **공격 표면 축소**: Docker(cgroups, namespace)로 한 번, isolate로 한 번, 그리고 Executor/Orchestrator/Watcher 권한 분리로 한 번 더 — 다층 방어 구조. 오케스트레이션 로직에 버그가 있어도 그 프로세스가 특권을 갖고 있지 않으므로 파급 범위가 제한됨.
- **유연한 확장성**: 이미지가 미리 준비돼 있어 컨테이너 복제 자체는 빠름.

### 🔴 단점 / 개선안

- **권한 설정의 복잡성**: Executor에 `SYS_ADMIN` 등 최소 캡ability만 부여하는 방향은 맞지만, 실제로는 cgroup v2 delegation(컨테이너 자신의 cgroup 서브트리 쓰기 권한 위임) 설정이 커널/Docker 버전에 따라 까다롭다. **배포 전 Lightsail Instance 환경에서 PoC로 반드시 검증**하고, 여의치 않으면 Executor 컨테이너만 `--privileged`로 격리(Orchestrator는 비특권 유지)하는 구조로 설정을 단순화.

- **오케스트레이션 코드 자체의 취약점 가능성**: 2024년 Judge0(동일한 "privileged 컨테이너 + isolate" 구조)에서 CVE-2024-28185, 28189, 29021이 발견됐다. 원인은 isolate 자체가 아니라 이를 감싸는 코드였다 — 샌드박스 밖에서 사용자 제어 가능한 경로의 심볼릭 링크를 그대로 따라가 임의 파일 쓰기가 가능했고, `--share-net` 옵션이 열려 있어 SSRF로 이어졌으며, 컨테이너가 `--privileged`였기 때문에 뚫리자마자 호스트 디스크 마운트까지 갔다. 코드 리뷰 시 symlink 처리와 쉘 인젝션 여지를 반드시 점검한다.

## 4. 참고자료

- https://github.com/ioi/isolate — Isolate 공식 레포지토리 및 샌드박싱 원리
- https://docs.docker.com/engine/security/ — Docker 컨테이너 보안, Linux Capabilities 및 런타임 권한 관리 가이드
- https://learn.microsoft.com/en-us/azure/architecture/patterns/compute-resource-consolidation — 워커 풀(Worker Pool) 패턴
- https://tantosec.com/blog/judge0/ — Judge0 Sandbox Escape (CVE-2024-28185, 28189, 29021) 분석: 동일 구조의 실제 취약점 사례