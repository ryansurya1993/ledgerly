# Multi-stage build: compile with the full Go toolchain, run from a
# minimal Alpine image that only carries the resulting binary (plus
# ca-certificates and wget, needed for outbound TLS and the
# HEALTHCHECK below respectively). See ledger-service/Dockerfile for
# why Alpine over distroless.
FROM golang:1.27.1-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/notification-service ./cmd/notification-service

FROM alpine:3.21

RUN apk add --no-cache ca-certificates wget

COPY --from=builder /out/notification-service /usr/local/bin/notification-service

EXPOSE 8082

HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=5 \
  CMD wget -q --spider "http://localhost:${PORT:-8082}/health" || exit 1

ENTRYPOINT ["notification-service"]
