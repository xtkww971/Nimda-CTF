#  executor 바이너리 빌드
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.work ./
COPY worker/go.mod worker/go.sum worker/
COPY executor/go.mod executor/
COPY protocol/ protocol/
COPY executor/ executor/
RUN cd executor && go build -o /out/executor ./cmd/executor

# 운영체제 선택
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# 필수 패키지 및 언어 런타임 설치(채점 언어 컴파일러 추가)
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential pkg-config libcap-dev libseccomp-dev libsystemd-dev ca-certificates git curl \
    python3 openjdk-21-jdk-headless \
    && rm -rf /var/lib/apt/lists/*

# Isolate 소스코드 설치 및 빌드
RUN git clone --depth 1 https://github.com/ioi/isolate.git /tmp/isolate && \
    cd /tmp/isolate && \
    make isolate && \
    make install && \
    rm -rf /tmp/isolate

# subid_user 계정 생성(Isolate가 접근할 수 있게 설정)
RUN useradd -r -s /usr/sbin/nologin isolate && \
    usermod -v 100000-165535 isolate && \
    usermod -w 100000-165535 isolate

# Isolate에게 cg 권한 부여
RUN sed -i 's|^cg_root = .*|cg_root = /sys/fs/cgroup|' /usr/local/etc/isolate

# UDS 소켓 위치 설정 
RUN mkdir -p /var/run/judge /app

WORKDIR /app

COPY executor/entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

COPY --from=builder /out/executor /app/executor

ENTRYPOINT ["/app/entrypoint.sh"]
