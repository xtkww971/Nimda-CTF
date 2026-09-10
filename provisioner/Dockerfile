# 빌드 컨텍스트는 리포 루트여야 한다 (docker-compose.yml에서 context: . 로 지정).
FROM golang:1.26-bookworm AS builder
WORKDIR /src
# protocol은 provisioner가 replace로 참조하므로 같이 들어와야 한다.
COPY protocol/ protocol/
COPY provisioner/ provisioner/
# go.work를 쓰면 여기 없는 모듈(agent)까지 찾으므로 끈다. 각 모듈의 replace로 충분하다.
ENV GOWORK=off
RUN cd provisioner && go build -o /out/provisioner ./cmd/provisioner

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /out/provisioner /app/provisioner
ENTRYPOINT ["/app/provisioner"]
