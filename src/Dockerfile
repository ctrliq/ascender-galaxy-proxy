FROM golang:1.25 AS builder

ENV CGO_ENABLED=0

COPY go.mod go.sum ascender_galaxy_proxy.go /app/

WORKDIR /app
RUN go mod download
RUN go build -o ascender_galaxy_proxy ascender_galaxy_proxy.go

###############################################################################
FROM alpine:3.23

RUN mkdir -p /app/.cache
COPY --from=builder /app/ascender_galaxy_proxy /app/ascender_galaxy_proxy

RUN chown -R 10001:10001 /app && chmod -R 755 /app

WORKDIR /app
EXPOSE 80
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:80/healthz || exit 1
USER 10001:10001
ENTRYPOINT ["/app/ascender_galaxy_proxy"]