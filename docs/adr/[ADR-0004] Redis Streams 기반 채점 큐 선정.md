* **날짜:** 2026-07-15
* **작성자:** 이도현
* **상태:** 수락됨

## 1. Context (배경)
채점 요청을 웹 백엔드(nimda-cite, producer)에서 채점 서버(Go Worker, consumer)로 비동기 전달할
producer-consumer 큐가 필요함. 기존 `README.md` 뱃지와 `docs/adr/README.md`의 ADR 파일명 예시
(`0002-use-rabbitmq-for-message-queue.md`)에는 **RabbitMQ**가 전제되어 있었으나, 실제로는 아직
RabbitMQ 인프라가 구축되지 않았고 별도 ADR로 확정된 적도 없었음. 반면 nimda-cite 백엔드는 이미
Redis(캐시, 인증코드 저장 등)를 운영 중이며, `docker-compose.yml`에 `nimda-redis` 컨테이너가
떠 있음.

고려 사항:
* 새 브로커 인프라(RabbitMQ) 도입 비용 대비, 이미 떠 있는 Redis 재사용 시의 운영 단순성.
* 채점 요청은 순서 보장이 크게 중요하지 않고, 유실 없이 최소 1회(at-least-once) 처리되면 충분함.
* 채점 서버가 크래시하거나 네트워크가 끊겨도 제출물이 유실되지 않아야 함 (장애 대응).
* 워커를 여러 대로 수평 확장할 수 있어야 함.

## 2. Decision (결정)
메시지 큐로 **Redis Streams**(Consumer Group 기반)를 채택함. RabbitMQ 등 별도 브로커는 도입하지
않음. 근거는 [Redis 공식 Streams 문서](https://redis.io/docs/latest/develop/data-types/streams/)이며,
세부 커맨드별 근거는 `docs/redis-stream-queue-design.md`에 정리함.

* Stream Key: `judge:submissions`
* Consumer Group: `judge-workers`
* Dead-letter Stream: `judge:submissions:dead-letter`
* Producer: `backend-spring`의 `judgeServer.domain.submission.mq.RedisSubmissionProducer`
* Consumer: 본 저장소의 `worker/` (Go, `internal/queue.Consumer`)

`webController/`(빈 Spring Boot 껍데기, 실제 API 로직 없음)는 삭제함. 채점 서버는 당분간 REST
API 레이어 없이 Go 컨슈머 단독으로 구성됨 (ADR-0001의 "채점 서버(Go Worker)" 역할만 남음).

## 3. Consequences (결과 및 영향)

### 🟢 장점
* **인프라 재사용:** 새 브로커를 띄우지 않고 기존 `nimda-redis` 컨테이너를 그대로 사용.
* **운영 단순성:** RabbitMQ의 vhost/exchange/binding 개념 없이, `XADD`/`XREADGROUP` 몇 개
  커맨드로 producer-consumer 패턴 구현.
* **장애 복구 내장:** Consumer Group의 Pending Entries List(PEL) + `XCLAIM`/`XAUTOCLAIM`으로
  워커 크래시 시에도 메시지가 유실되지 않고 자동 재할당됨 (자세한 내용은 설계 문서 참고).
* **수평 확장 용이:** 같은 그룹에 컨슈머 이름만 다르게 하여 워커 프로세스를 추가하면 자동으로
  메시지가 분산됨.

### 🔴 단점 / 개선안
* **네이티브 DLQ 부재:** RabbitMQ와 달리 Redis Streams는 Dead Letter Queue를 기본 제공하지
  않음. → `deliveryCount` 초과 시 별도 `judge:submissions:dead-letter` 스트림으로 수동
  이관하는 방식으로 구현 (`worker/internal/queue/consumer.go`의 `deadLetter`).
* **무제한 스트림 증가:** ACK된 엔트리도 자동 삭제되지 않음. 운영 전 `XADD ... MAXLEN ~`
  트리밍 정책을 producer 또는 별도 배치에 추가해야 함 (미구현, 후속 작업).
* **브로커 자체의 이중화 없음:** 현재 Redis는 단일 인스턴스. Redis 자체가 죽으면 큐 전체가
  영향을 받음 → 후속으로 Redis Sentinel/Cluster 도입 검토 필요 (미구현, 후속 작업).
* **README 뱃지 불일치:** 최상위 `README.md`는 아직 RabbitMQ 뱃지를 표시하고 있음. 본 ADR
  반영 후 README 갱신 필요 (범위상 이번 작업에서는 보류).

### 참고자료
* [Redis Streams 소개](https://redis.io/docs/latest/develop/data-types/streams/)
* [Redis Streams 예제: 신뢰할 수 있는 큐 만들기](https://redis.io/docs/latest/develop/data-types/streams/#reliability-and-recovering-from-crashes)
* [XGROUP CREATE](https://redis.io/docs/latest/commands/xgroup-create/)
* [XREADGROUP](https://redis.io/docs/latest/commands/xreadgroup/)
* [XPENDING](https://redis.io/docs/latest/commands/xpending/)
* [XCLAIM](https://redis.io/docs/latest/commands/xclaim/) / [XAUTOCLAIM](https://redis.io/docs/latest/commands/xautoclaim/)