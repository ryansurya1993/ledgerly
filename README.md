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

(or `docker compose up` — either the standalone tool or the Docker CLI
plugin works with the `docker-compose.yml` in this repo.)

This brings up the full stack: Postgres, PgBouncer (sitting in front
of it — see `ledger-service/README.md`'s "PgBouncer" section for why),
Redis, and RabbitMQ, plus `ledger-service`, `wallet-service`
(http://localhost:8081), and `notification-service`
(http://localhost:8082 — `GET /events` is a live Server-Sent Events
feed of every transaction as it posts; try `curl -N
localhost:8082/events` in one terminal while you top up or transfer in
another), each built from its own `Dockerfile`. Startup is
dependency-ordered by real health-check conditions in
`docker-compose.yml`, not just container-started ordering:
`wallet-service` waits on `ledger-service`, `ledger-service` waits on
both Postgres and PgBouncer, and both `ledger-service` and
`notification-service` wait on RabbitMQ — all waiting on their
respective dependencies reporting genuinely healthy. No manual setup
step is required first: `ledger-service` provisions its own restricted
database role's password on every startup (see
`ledger-service/README.md`'s "First-time setup" for why that's not
just baked into a migration file).

Each service's own README has its full setup, environment variables,
and API docs.

**Postgres, PgBouncer, Redis, RabbitMQ, and `ledger-service` aren't
reachable directly from your host by default** — only `wallet-service`
(the visitor-facing gateway, and what serves the frontend below) and
`notification-service` (the live feed) publish a host port, matching
what a production setup would actually expose. `ledger-service` joins
that list for a second reason beyond the general policy: a fixed host
port for it would conflict with itself the moment you run more than
one replica (see "Prove it scales yourself" below) — every service
still reaches all of these over Compose's internal network by service
name regardless of host publishing. To poke at any of them directly for
debugging:

```bash
docker compose exec postgres psql -U postgres -d ledgerly
docker compose exec redis redis-cli
docker compose exec wallet-service wget -qO- http://ledger-service:8080/health
```

(`ledger-service` has no published port at all, not even an
overridable one — `docker compose port` only reports *published*
ports, so reaching it directly means curling it from inside another
container on the network, as above, or adding a port mapping via
`docker-compose.override.yml` yourself.)

Or, to get real host-published ports back (e.g. to point a GUI client
at Postgres, or open RabbitMQ's management UI), copy
`docker-compose.override.yml.example` to `docker-compose.override.yml`
(gitignored, loaded automatically) — see that file and
`docker-compose.yml`'s own comment for why it isn't the default.

## Frontend

Open **http://localhost:8081/** once the stack is up — that's the
whole demo UI: create a wallet, top it up, transfer between wallets,
and run the stress-test panel described below.

> 🖼️ *(placeholder)*

The frontend is plain HTML/CSS/vanilla JS in [`frontend/`](frontend) at
the repo root — no build step, no framework, no npm dependencies, per
`PROJECT_BRIEF.md`: this is a backend portfolio piece, and the frontend
only needs to be clear and demoable, not impressive in its own right.

**It's served by `wallet-service`, not a separate static server.**
`cmd/wallet-service/main.go` registers `frontend/`'s files as the
catch-all route behind its API endpoints (see `FRONTEND_DIR` in
`wallet-service/README.md`). The alternative — a standalone static file
server on its own port — would need CORS headers added to
wallet-service for every single API call the page makes (creating a
wallet, topping up, transferring, reading history), since the page and
the API would then be on different origins. Serving both from the same
origin sidesteps that entirely: every call the page makes to
wallet-service is same-origin, so the *only* cross-origin request this
page ever makes is the live activity feed's connection to
`notification-service:8082`, which already sets
`Access-Control-Allow-Origin: *` for exactly this reason (see
`notification-service/README.md`). In `docker-compose.yml`,
`frontend/` is bind-mounted into the `wallet-service` container rather
than baked into its image, so editing a frontend file takes effect on
the next browser refresh — no rebuild, no restart. Running
`wallet-service` directly with `go run` (outside Docker) picks up
`../frontend` the same way, via `FRONTEND_DIR`'s default.

### What's on the page

- **Wallet management** — create a wallet, pick one from a dropdown
  populated live from `GET /wallets` (see wallet-service's README's API
  section) or load one by ID, see its balance and history, top it up,
  and transfer to another wallet. `localStorage` only remembers which
  wallet you last had selected, as a reload convenience — the list of
  wallets that actually exist always comes from that endpoint, never
  from the browser, so a wallet from before a `docker-compose down -v`
  reset just quietly stops appearing rather than needing any special
  recovery.
- **Stress-test panel** — five demonstrations, each proving something
  different:
  - **(a) Fire N idempotent replays** and **(b) Fire N concurrent
    transfers** are deliberately presented side by side but are *not*
    the same test: (a) fires N requests sharing *one* idempotency key
    to prove duplicates collapse into a single transaction; (b) fires N
    requests each with a *different* key to prove genuinely concurrent
    writes to the same account all still apply, with none lost. Each
    shows the numbers that prove its own claim (unique transaction IDs
    returned; expected vs. actual final balance) rather than just a
    pass/fail label.
  - **(c) Try to overdraft** — shows the rejection's actual error
    message, not a raw JSON dump.
  - **(d) Replay last transaction** — resubmits your most recent
    top-up/transfer with its original idempotency key.
  - A live **integrity badge** polls `GET /integrity` (proxied through
    `wallet-service` — see its README's API section — purely so the
    page never has to deal with ledger-service as a third origin) every
    few seconds.
- **Live activity feed** — every transaction posted anywhere, streamed
  in as it happens via `notification-service`'s SSE endpoint.

### Prove it scales yourself

This project runs on a single small server for the live demo — but the
application code underneath is genuinely stateless (see "Design
principle: statelessness" above — re-verified directly against the
source, not just asserted, before writing this section): no in-memory
cache of a balance, a session, or an idempotency key anywhere in
`ledger-service` or `wallet-service`. Every correctness guarantee
(no lost updates under concurrent writes, no duplicate transaction from
a replayed request) lives in Postgres or Redis, not in any one
replica's memory — which is exactly what makes scaling either service
to N replicas safe rather than just possible.

Bring up multiple replicas of both:

```bash
docker-compose -f docker-compose.yml -f docker-compose.scale.yml \
  up -d --scale ledger-service=3 --scale wallet-service=5
```

That second `-f` matters, and is the one deliberate wrinkle in an
otherwise one-line command: `wallet-service`'s port is fixed at 8081 in
the base file specifically so the frontend has one stable address to
talk to (see "Frontend" above), but Compose can't bind 5 replicas to
that same fixed host port at once. `docker-compose.scale.yml` swaps it
for an unfixed one so each replica gets its own — see that file's own
comment for the full reasoning, including why this means the frontend
itself isn't part of this particular proof: a true single-URL,
browser-facing view of 5 load-balanced replicas needs an actual load
balancer in front of them, which is real, not-yet-built future work
(see "Scaling Roadmap" below), not something this overlay tries to
fake. `ledger-service` needed no such override — it was never
published to the host at all (see "Running Locally" above), so
`--scale ledger-service=3` was already safe.

Confirm multiple replicas of each are actually up and independently
reachable:

```bash
docker-compose ps
# hit two different wallet-service replicas directly by their own assigned ports:
curl localhost:$(docker compose port --index=1 wallet-service 8081 | cut -d: -f2)/health
curl localhost:$(docker compose port --index=2 wallet-service 8081 | cut -d: -f2)/health
```

Then fire real concurrent traffic at the scaled stack and confirm
correctness held throughout:

```bash
docker-compose -f docker-compose.yml -f docker-compose.loadtest.yml \
  run --rm k6
```

See [`k6/loadtest.js`](k6/loadtest.js) for exactly what it does (ramps
up to 20 virtual users creating, funding, and balance-checking fresh
wallets against `wallet-service` by service name — Docker's own
embedded DNS round-robins that across however many replicas are
running, no load balancer needed for this non-browser purpose) and
[`docker-compose.loadtest.yml`](docker-compose.loadtest.yml) for how it
attaches to the same network. Watch `GET /integrity` throughout (via
the frontend's badge, pointed at whichever single `wallet-service`
replica the frontend itself is talking to, or `curl` any replica's
`/integrity` directly) — it should read `"drifted": false` before,
during, and after the load test, regardless of which replica handled
which request in between.

Tear the scaled stack back down the same way you brought it up:

```bash
docker-compose -f docker-compose.yml -f docker-compose.scale.yml down
```

## Scaling Roadmap

This project intentionally uses the smallest tool that fits its current
scope. Here's how it would evolve to handle real fintech-scale traffic:

- **A real load balancer in front of `wallet-service`.** "Prove it
  scales yourself" above already demonstrates that running multiple
  `wallet-service`/`ledger-service` replicas is *safe* (genuinely
  stateless application code, Postgres/Redis as the only sources of
  truth) and that they can be reached and load-tested once scaled — but
  each `wallet-service` replica currently ends up on its own
  Docker-assigned host port (`docker-compose.scale.yml`), not one
  shared address. A visitor-facing demo of *that* — one stable URL, N
  replicas behind it, kill one and watch traffic keep flowing — needs
  an actual load balancer (nginx, Traefik, or, on the k3s deployment
  target, just a Kubernetes `Service`) in front, which is real,
  deliberately deferred work, not a gap in the underlying design.
- **Best-effort event publishing → transactional outbox.**
  `ledger-service` currently publishes `transaction.posted` events
  best-effort, after its own database transaction commits — see its
  README's "Events" section — because `notification-service`'s live
  feed is a nice-to-have view, not something anything else depends on
  for correctness. If a future consumer of these events needs
  at-least-once delivery (e.g. audit logging, fraud detection, billing)
  that's the signal to add an outbox table written in the same
  transaction as the ledger entries, plus a relay process that retries
  until RabbitMQ confirms receipt — not to retrofit stronger guarantees
  onto the activity feed's existing best-effort path.
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
