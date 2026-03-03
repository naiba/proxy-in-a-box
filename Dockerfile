# Build proxy-in-a-box
FROM golang:1.23-alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags="-s -w" -o proxy-in-a-box ./cmd/proxy-in-a-box

# Runtime
FROM alpine:latest

RUN apk add --no-cache \
    chromium \
    nss \
    freetype \
    harfbuzz \
    ca-certificates \
    ttf-freefont \
    curl \
    dumb-init

# 安装 pinchtab 二进制
ARG TARGETARCH
RUN ARCH=$(case "${TARGETARCH}" in amd64) echo "amd64";; arm64) echo "arm64";; *) echo "amd64";; esac) && \
    curl -fsSL -o /usr/local/bin/pinchtab \
    "https://github.com/pinchtab/pinchtab/releases/latest/download/pinchtab-linux-${ARCH}" && \
    chmod +x /usr/local/bin/pinchtab

# 复制 proxy-in-a-box 二进制
COPY --from=builder /build/proxy-in-a-box /usr/local/bin/proxy-in-a-box
WORKDIR /app

ENV CHROME_BIN=/usr/bin/chromium-browser

EXPOSE 8080 8081 8083

ENTRYPOINT ["/usr/bin/dumb-init", "--"]
CMD ["proxy-in-a-box", "-c", "/app/data/pb.yaml"]
