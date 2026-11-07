# The atv-bridge image. Copied from stone-age-io/services' Dockerfile, with
# the service fixed to atv-bridge.
#
#   docker build -t atv-bridge .
#   docker run -d --name atv-bridge \
#     -v /etc/atv-bridge/config.yaml:/etc/atv-bridge/config.yaml:ro \
#     -v atv-creds:/creds:ro -v atv-session:/session \
#     -p 8080:8080 \
#     atv-bridge

# --platform=$BUILDPLATFORM: with CGO_ENABLED=0 Go cross-compiles for free, so
# the compiler runs natively and only the tiny runtime stage is per-arch.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=0.0.0-dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/service ./cmd/atv-bridge

# Alpine rather than distroless for busybox wget, which the HEALTHCHECK needs.
FROM alpine:3
# The uid and gid are fixed, so a creds-keeper and the bridge can share a
# creds file across containers. /creds and /session belong to the service
# user, so an empty named volume mounted there is writable by it. /session
# holds the client certificate every TV is paired with: keep it on a volume.
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 service \
    && adduser -D -H -u 10001 -G service service \
    && mkdir /creds /session \
    && chown service:service /creds /session
COPY --from=build /out/service /service
USER service

# /api/ready and /metrics on HTTP_ADDR, :8080 by default.
EXPOSE 8080

# Marks the container unhealthy on a 503; it does not restart it.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -q -O /dev/null "http://127.0.0.1:8080/api/ready" || exit 1

ENTRYPOINT ["/service"]
