# Multi-stage build: compile with the full Go toolchain, run from a
# minimal Alpine image that only carries the resulting binary (plus
# ca-certificates and wget, needed for outbound TLS and the
# HEALTHCHECK below respectively). Alpine over distroless because the
# HEALTHCHECK needs a shell + wget to run inside the container; a
# distroless/scratch image would need a separate compiled health-check
# binary just to get that back, which isn't worth it at this project's
# current scope.
FROM golang:1.27.1-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/ledger-service ./cmd/ledger-service

FROM alpine:3.21

RUN apk add --no-cache ca-certificates wget

COPY --from=builder /out/ledger-service /usr/local/bin/ledger-service

EXPOSE 8080

HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=5 \
  CMD wget -q --spider "http://localhost:${PORT:-8080}/health" || exit 1

ENTRYPOINT ["ledger-service"]
