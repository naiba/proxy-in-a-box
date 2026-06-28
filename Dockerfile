# Build proxy-in-a-box (pure Go with glebarez/sqlite, no CGO — Alpine is fine)
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o proxy-in-a-box ./cmd/proxy-in-a-box

# Debian slim: Obscura release binaries are glibc-linked, so Alpine/musl is not compatible.
FROM debian:bookworm-slim

ARG TARGETARCH
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       ca-certificates \
       curl \
       tini \
    && rm -rf /var/lib/apt/lists/* \
    && BUILD_ARCH=${TARGETARCH:-$(uname -m)} \
    && ARCH=$(echo "${BUILD_ARCH}" | sed 's/amd64/x86_64/;s/arm64/aarch64/') \
    && mkdir -p /tmp/obscura \
    && curl -fsSL -o /tmp/obscura.tar.gz \
       "https://github.com/h4ckf0r0day/obscura/releases/latest/download/obscura-${ARCH}-linux.tar.gz" \
    && tar -xzf /tmp/obscura.tar.gz -C /tmp/obscura \
    && install -m 0755 /tmp/obscura/obscura /usr/local/bin/obscura \
    && install -m 0755 /tmp/obscura/obscura-worker /usr/local/bin/obscura-worker \
    && rm -rf /tmp/obscura /tmp/obscura.tar.gz

COPY --from=builder /build/proxy-in-a-box /usr/local/bin/proxy-in-a-box
COPY docker-entrypoint.sh /usr/local/bin/
WORKDIR /app
RUN mkdir -p /app/data

EXPOSE 8080 8081 8083

# BUG-FIX: 不能在此处 USER 65534，因为 volume 挂载会覆盖构建阶段的 chown。
# entrypoint 以 root 启动修复权限后再降权到 nobody(65534)。
ENTRYPOINT ["/usr/bin/tini", "--", "docker-entrypoint.sh"]
CMD ["proxy-in-a-box", "-c", "/app/data/pb.yaml"]
