# 빌드 컨텍스트는 리포 루트여야 한다 (docker-compose에서 context: . 로 지정).
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY protocol/ protocol/
COPY dashboard/ dashboard/
# go.work를 쓰면 여기 없는 모듈(agent, provisioner)까지 찾으므로 끈다. replace로 충분하다.
ENV GOWORK=off
RUN cd dashboard && CGO_ENABLED=0 go build -o /out/dashboard ./cmd/dashboard

# ctf-redis에서 snapshot 하나를 읽어 보여주는 게 전부라 셸도 필요 없다.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/dashboard /usr/local/bin/dashboard
EXPOSE 6070
ENTRYPOINT ["/usr/local/bin/dashboard"]
