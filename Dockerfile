# Build proxy-in-a-box (pure Go with glebarez/sqlite, no CGO — Alpine is fine)
FROM golang:1.26.6-alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o proxy-in-a-box ./cmd/proxy-in-a-box

# Debian slim: Obscura release binaries are glibc-linked, so Alpine/musl is not compatible.
FROM debian:trixie-slim

ARG TARGETARCH
ARG OBSCURA_VERSION=v0.2.1
RUN set -eux; \
    apt-get update \
    && apt-get install -y --no-install-recommends \
       ca-certificates \
       curl \
       tini \
    && rm -rf /var/lib/apt/lists/* \
    && BUILD_ARCH=${TARGETARCH:-$(uname -m)} \
    && ARCH=$(echo "${BUILD_ARCH}" | sed 's/amd64/x86_64/;s/arm64/aarch64/') \
    && case "${ARCH}" in \
         x86_64) OBSCURA_SHA256=49856870420960ce489d2d1ff40fffac5b8c016604b9af0ded8ed6373abd9302 ;; \
         aarch64) OBSCURA_SHA256=77704cf11a0a4f4849d93501e1f2a3ff09ca62e2700049ac9c6e83922b86828a ;; \
         *) echo "unsupported architecture: ${ARCH}" >&2; exit 1 ;; \
       esac \
    && mkdir -p /tmp/obscura \
    && curl -fsSL -o /tmp/obscura.tar.gz \
       "https://github.com/h4ckf0r0day/obscura/releases/download/${OBSCURA_VERSION}/obscura-${ARCH}-linux-stealth.tar.gz" \
    && echo "${OBSCURA_SHA256}  /tmp/obscura.tar.gz" | sha256sum -c - \
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
