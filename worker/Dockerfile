# 빌드 컨텍스트는 리포 루트여야 한다 (docker-compose.yml에서 context: . 로 지정).
# worker 모듈이 워크스페이스 로컬 모듈인 nimda-judge-protocol을 import하기 때문에
# go.work와 protocol/ 디렉토리가 같이 보여야 빌드된다.
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.work ./
COPY worker/go.mod worker/go.sum worker/
COPY executor/go.mod executor/
COPY protocol/ protocol/
COPY worker/ worker/
RUN cd worker && go build -o /out/worker ./cmd/worker
RUN cd worker && go build -o /out/smoketest ./cmd/smoketest

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /out/worker /app/worker
COPY --from=builder /out/smoketest /app/smoketest
ENTRYPOINT ["/app/worker"]
