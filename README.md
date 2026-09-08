# Ledgerly (Work In Progress)

A double-entry ledger with a wallet-style interface, built in Go to
demonstrate correctness under concurrency, idempotent request handling,
and horizontally scalable service design.

**[Live demo →](#)** &nbsp;|&nbsp; **[Run it yourself with docker-compose →](#running-locally)**

---

## What This Is

Ledgerly looks like a simple wallet app — create an account, top it up,
transfer funds, see your balance and history. Underneath, every
transaction is recorded using **double-entry accounting**, the same
principle real payment infrastructure (Stripe, banking cores) is built
on: every transaction has a debit leg and a credit leg, and the sum of
all debits always equals the sum of all credits, system-wide.

The demo includes a **stress-test panel** where you can try to break it:
attempt to overdraft an account, fire concurrent transfers at the same
account, replay a transaction to test idempotency, and watch a live
integrity check confirm the books always balance.

Each service has its own README.md with setup instructions, API docs,
and first-time configuration steps (e.g. setting up database roles).
Please read each service's README before running it.

## Architecture

Three services, kept intentionally small in number so each one is done
well rather than spreading logic thin:

- **`ledger-service`** — the core: accounts, double-entry transactions,
  balance calculation, integrity checks. Owns all financial state.
- **`wallet-service`** — the visitor-facing API. Wraps ledger operations
  into wallet actions (top-up, transfer, balance), and handles
  idempotency keys.
- **`notification-service`** — consumes transaction events and drives
  the live activity feed on the frontend.

Supporting infrastructure: PostgreSQL (source of truth), PgBouncer
(connection pooling), Redis (shared idempotency store), RabbitMQ (event
propagation).

### Design principle: statelessness

Every application service is stateless — no account balance or session
data is cached in a service's own memory. This is what actually makes
horizontal scaling possible: any number of replicas can sit behind a
load balancer and behave identically, because they all read from and
write to the same Postgres instance, and idempotency keys live in a
shared Redis store rather than per-instance memory. Concurrency
correctness (e.g., two transfers hitting the same account at once) is
handled with optimistic concurrency / row-level locking in Postgres —
not by the message broker or any in-memory coordination.

## Running Locally

```bash
docker-compose up
```

Runs one instance of each service, backed by Postgres, Redis, and
RabbitMQ.

### Prove it scales yourself

This project runs on a single small server for the live demo — but the
architecture is designed to scale horizontally, and you can verify that
yourself without needing real production infrastructure:

```bash
docker-compose up --scale wallet-service=5 --scale ledger-service=3
```

Then run the bundled load test against the scaled stack:

```bash
docker-compose -f docker-compose.loadtest.yml run k6
```

Watch the integrity check endpoint throughout — balances stay correct
and no updates are lost, regardless of which replica handled which
request.

## Scaling Roadmap

This project intentionally uses the smallest tool that fits its current
scope. Here's how it would evolve to handle real fintech-scale traffic:

- **RabbitMQ → Kafka.** As transaction volume and the number of
  downstream event consumers (audit logs, fraud detection, analytics)
  grow, Kafka's log-based retention and higher sustained throughput
  become the better fit. Migration path: introduce a bridge or dual-write
  during transition, move producers over topic-by-topic.
- **Single Postgres → read replicas / CQRS.** Balance and history reads
  (the highest-volume operation) would be split from the transactional
  write path, served by read replicas or a dedicated read model.
- **Sharding by account ID.** At sufficient scale, accounts would be
  partitioned across multiple database shards so no single instance
  holds the entire ledger.
- **Multi-region deployment.** For genuine international traffic, this
  moves from a single k3s node to a multi-node Kubernetes cluster
  distributed across regions, with an explicit consistency model:
  eventual consistency acceptable for non-critical reads, strong
  consistency preserved on the ledger write path.
- **Distributed tracing and metrics** (OpenTelemetry, Prometheus/Grafana)
  once requests span multiple services and regions.

## Deployment

Deployed on a single VPS using [k3s](https://k3s.io/), a lightweight,
production-grade Kubernetes distribution built for exactly this kind of
constrained environment. CI/CD via GitHub Actions: build → push to
GitHub Container Registry → deploy to k3s.

## Built with Claude Code

This project was built using [Claude Code](https://claude.com/claude-code)
as part of the development workflow:

- **Scaffolding** — service structure, Dockerfiles, and Kubernetes
  manifests were generated and iterated on with Claude Code.
- **Tests** — the concurrency test suite (simulating simultaneous
  transfers against the same account to verify no lost updates) was
  built with Claude Code's help, since these are tedious to hand-write
  but essential to have.
- **Core logic** — the ledger's transaction posting, balance
  calculation, and concurrency handling were designed and implemented
  collaboratively, with every design decision reviewed and understood
  rather than accepted as-is, since this is the part of the system that
  needs to hold up under technical scrutiny.

A `CLAUDE.md` file in this repo documents the conventions and commands
used throughout the project.

## Tech Stack

Go &middot; PostgreSQL &middot; PgBouncer &middot; Redis &middot;
RabbitMQ &middot; Docker &middot; k3s &middot; GitHub Actions

## License

MIT
