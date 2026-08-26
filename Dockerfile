# Build stage. The Go version matches the `go` directive in go.mod, and the
# SQLite driver is pure Go, so the binary is cross-compiled with CGO disabled.
FROM --platform=${BUILDPLATFORM:-linux/amd64} golang:1.22.5-alpine AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

# Dependency manifests are copied first so the module download layer is cached.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOTOOLCHAIN=local GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
        go build -trimpath -ldflags="-s -w" \
        -o /out/orientation-server ./cmd/server

# Runtime stage.
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S orientation \
    && adduser -S -G orientation orientation \
    && mkdir -p /data \
    && chown -R orientation:orientation /data

COPY --from=builder /out/orientation-server /usr/local/bin/orientation-server

WORKDIR /app
USER orientation

ENV APP_HTTP_ADDR=":8080" \
    APP_DATABASE_DSN="file:/data/orientation.db?_pragma=busy_timeout(10000)" \
    APP_LOG_LEVEL="info" \
    APP_BUSINESS_TZ="Asia/Shanghai" \
    GOTOOLCHAIN="local"

EXPOSE 8080
VOLUME ["/data"]

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
    CMD wget -q -O- http://127.0.0.1:8080/readyz || exit 1

ENTRYPOINT ["/usr/local/bin/orientation-server"]
