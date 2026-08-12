# provisioner 테스트

이 프로젝트의 테스트는 두 종류로 나뉜다.

| 종류 | 위치 | 외부 의존 | 특징 |
|---|---|---|---|
| **단위 테스트** | 각 패키지 옆 (`internal/**/​*_test.go`) | 없음 | 빠르고 항상 실행 가능 |
| **통합 테스트** | 이 폴더 (`test/`) | 실제 Redis · AWS S3 | 준비물이 없으면 자동으로 **skip** |

> **왜 단위 테스트는 여기로 안 모았나:** Go는 `_test.go` 파일이 테스트하는 코드와 **같은 폴더**에
> 있어야 한다. 특히 `internal/queue/backoff_test.go`는 비공개 함수(`newBackoff`)를 테스트해서
> 옮기면 컴파일이 깨진다. 그래서 외부 서비스가 필요한 통합 테스트만 이쪽으로 모았다.

---

## 빠른 실행

```bash
cd provisioner

# 1) 단위 테스트만 (Redis/AWS 없이도 항상 통과)
go test ./internal/...

# 2) 통합 테스트 (아래 준비물 필요 — 없으면 skip)
go test ./test/ -v

# 3) 전부
go test ./...
```

---

## 통합 테스트 준비물

### Redis (큐 송수신 테스트)

`TestReceiveRequestAndPublishResult`, `TestPoisonRequestGoesToDeadLetter`가 쓴다.
로컬 6379와 겹치지 않게 **6380** 포트로 띄운다.

```bash
docker run -d --name ctf-redis-test -p 6380:6379 redis:7-alpine

# 테스트가 끝나면
docker rm -f ctf-redis-test
```

Redis가 없으면 두 테스트는 skip된다.

### AWS S3 (다운로드·압축 해제 테스트)

`TestS3PingBucket`, `TestGetFile`, `TestDownloadAndUnzipFromS3`가 쓴다.
`config.Load()`가 자격증명을 읽는데, 아래 값이 환경변수 또는 `provisioner/.env`에 있어야 한다.

```
AWS_S3_ACCESS_KEY=...
AWS_S3_SECRET_KEY=...
AWS_S3_REGION=ap-northeast-2
AWS_S3_BUCKET=nimda-website
```

자격증명이 없으면 세 테스트는 skip된다.

`TestGetFile`·`TestDownloadAndUnzipFromS3`는 버킷에 실제 객체가 있어야 한다.
현재 기준 객체:

```
code = web-basic-01  ->  challenges/web-basic-01/web-basic-01.zip
```

객체가 없으면(404) 해당 테스트만 skip된다.

---

## 테스트별 요약

| 테스트 | 확인하는 것 |
|---|---|
| `TestS3PingBucket` | S3 버킷에 자격증명으로 접근되는지 |
| `TestGetFile` | 문제 zip 하나를 S3에서 받아오는지 |
| `TestDownloadAndUnzipFromS3` | 다운로드 → 압축 해제 → 작업 폴더에 파일이 실제로 들어가는지 |
| `TestReceiveRequestAndPublishResult` | 큐 요청을 받아 → 처리 → 결과를 결과 스트림에 발행하고 ACK하는지 |
| `TestPoisonRequestGoesToDeadLetter` | 잘못된 요청이 무한 재시도되지 않고 dead-letter로 가는지 |
