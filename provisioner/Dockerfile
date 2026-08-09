# 빌드 컨텍스트는 리포 루트여야 한다 (docker-compose.yml에서 context: . 로 지정).
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.work ./
COPY provisioner/go.mod provisioner/go.sum provisioner/
COPY provisioner/ provisioner/
RUN cd provisioner && go build -o /out/provisioner ./cmd/provisioner

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /out/provisioner /app/provisioner
ENTRYPOINT ["/app/provisioner"]
