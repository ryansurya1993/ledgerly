# CLAUDE.md

This file is read automatically by Claude Code at the start of a
session to provide project context.

## Project Overview

Ledgerly is a Go microservices project implementing a double-entry
ledger with a wallet-style interface (top-up, transfer, balance,
history). It's built to demonstrate correctness under concurrency,
idempotent request handling, and horizontally scalable service design,
deployed via Docker and k3s (lightweight Kubernetes).

## Architecture

Three services:
- `ledger-service` — core double-entry ledger: accounts, transactions,
  balance calculation, integrity checks. Owns all financial state in
  Postgres.
- `wallet-service` — visitor-facing API gateway. Wraps ledger
  operations into wallet actions (top-up, transfer, balance). Handles
  idempotency keys via Redis.
- `notification-service` — consumes transaction events from RabbitMQ,
  drives the live activity feed on the frontend via WebSocket/SSE.

Supporting infrastructure (not "our" services): PostgreSQL, PgBouncer,
Redis, RabbitMQ.

## Development Guidelines

- All application services must remain **stateless** — no in-memory
  caching of account balances or session data. Postgres is the single
  source of truth. This is a deliberate design constraint, not an
  oversight — see README.md's Architecture section for why.
- Idempotency keys are stored in Redis (shared across replicas), never
  in local/in-memory state.
- Account updates use optimistic concurrency (a `version` column) or
  row-level locking — never last-write-wins without a check.
- Never delete or mutate a posted ledger entry to "fix" a mistake.
  Corrections are always compensating entries (a new transaction that
  reverses the effect), preserving full audit history.
- Follow standard Go project layout (`cmd/`, `internal/`, `pkg/` as
  needed per service).
- Write tests alongside new logic, especially concurrency tests for
  anything touching account balances.

## Important Commands

- `docker-compose up` — run all services locally (single instance each)
- `docker-compose up --scale wallet-service=5 --scale ledger-service=3`
  — run multiple replicas to test horizontal scaling behavior
- `docker-compose -f docker-compose.loadtest.yml run k6` — run the
  bundled load test against a running stack
- `go test ./...` (run inside each service directory) — run unit and
  concurrency tests
- `make lint` — run linters (if Makefile present)

## Notes for Claude Code Sessions

- Scaffolding (service structure, Dockerfiles, k8s manifests,
  boilerplate handlers) is a good fit for full autonomy.
- Core ledger logic (transaction posting, balance calculation,
  concurrency handling) should be built collaboratively — explain
  reasoning, not just generate — since these decisions need to be
  defensible in technical interviews.
- When adding infrastructure, default to the smallest tool that fits
  current scope. If something heavier seems justified (e.g., swapping
  RabbitMQ for Kafka), document the reasoning in README.md's Scaling
  Roadmap section rather than silently upgrading.
