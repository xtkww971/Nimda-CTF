# 빌드 컨텍스트는 리포 루트여야 한다 (docker-compose에서 context: . 로 지정).
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY protocol/ protocol/
COPY agent/ agent/
# go.work를 쓰면 여기 없는 모듈(provisioner)까지 찾으므로 끈다. 각 모듈의 replace로 충분하다.
ENV GOWORK=off
RUN cd agent && CGO_ENABLED=0 go build -o /out/agent ./cmd/agent

# 예전에는 docker:cli를 베이스로 썼다. 에이전트가 docker 명령을 exec했기 때문인데,
# 지금은 데몬 소켓에 Go SDK로 직접 말하므로 CLI 바이너리가 필요 없다.
#
# 정적 링크된 바이너리 하나만 들어가므로 베이스는 빈 이미지로 충분하다. 셸도 패키지 매니저도
# 없어서, 문제 컨테이너를 만지는 이 특권 프로세스가 뚫렸을 때 쓸 도구가 남지 않는다.
#
# nonroot 태그를 쓰지 않는 이유: 호스트 도커 소켓은 보통 root:docker 660이라 비-root
# 사용자로는 못 연다. 어차피 도커 소켓에 닿는 프로세스는 호스트 root와 같은 힘을 가지므로
# (ADR-0009가 이 조각을 "특권 프로세스"라 부르는 이유다), 여기서 사용자만 낮추는 것은
# 실질적인 방어가 되지 않는다. 경계는 "누가 이 프로세스에 일을 시킬 수 있는가"에 둔다.
FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/agent /usr/local/bin/agent
ENTRYPOINT ["/usr/local/bin/agent"]
