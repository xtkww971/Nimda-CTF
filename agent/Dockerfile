# 빌드 컨텍스트는 리포 루트여야 한다 (docker-compose에서 context: . 로 지정).
#
# 운영 배포는 호스트 프로세스(systemd)가 기본이다(ADR-0009). 이 이미지는 개발/통합 테스트에서
# 한 번에 띄우기 위한 것으로, 호스트 도커 소켓을 마운트해 쓴다. 즉 컨테이너 안에 도커 데몬을
# 넣는 게 아니라 CLI만 넣고 데몬은 호스트 것을 쓴다.
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY protocol/ protocol/
COPY agent/ agent/
# go.work를 쓰면 여기 없는 모듈(provisioner)까지 찾으므로 끈다. 각 모듈의 replace로 충분하다.
ENV GOWORK=off
RUN cd agent && CGO_ENABLED=0 go build -o /out/agent ./cmd/agent

# docker:cli - 도커 CLI만 들어있는 이미지 (데몬 없음).
FROM docker:cli
COPY --from=builder /out/agent /usr/local/bin/agent
ENTRYPOINT ["/usr/local/bin/agent"]
