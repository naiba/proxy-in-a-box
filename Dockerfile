# Build proxy-in-a-box
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN go build -ldflags="-s -w -X main.version=${VERSION}" -o proxy-in-a-box ./cmd/proxy-in-a-box

# Runtime
FROM alpine:latest

RUN apk add --no-cache \
    ca-certificates \
    curl \
    tini \
    && ARCH=$(uname -m | sed 's/aarch64/aarch64/;s/x86_64/x86_64/') \
    && curl -fsSL -o /usr/local/bin/lightpanda \
       "https://github.com/lightpanda-io/browser/releases/download/nightly/lightpanda-${ARCH}-linux" \
    && chmod +x /usr/local/bin/lightpanda

COPY --from=builder /build/proxy-in-a-box /usr/local/bin/proxy-in-a-box
WORKDIR /app

EXPOSE 8080 8081 8083

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["proxy-in-a-box", "-c", "/app/data/pb.yaml"]
