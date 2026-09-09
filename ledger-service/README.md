# ledger-service

Core double-entry ledger: accounts, transactions, balance calculation,
integrity checks. Owns all financial state in Postgres. See the root
`README.md` and `CLAUDE.md` for the overall Ledgerly architecture.

## First-time setup

Set `LEDGER_APP_DB_PASSWORD` and `LEDGER_MIGRATE_DB_PASSWORD` in your
environment (see the table under "Environment variables" below), then
just start the service:

```bash
go run ./cmd/ledger-service
```

That's the whole setup. Nothing else needs to run by hand, and nothing
needs to run against Postgres out-of-band first.

**Why this isn't a manual step:** migration `000002_restrict_app_role.up.sql`
creates the `ledger_app` role with **no password** — a migration file
is version-controlled and readable by anyone with repo access, so a
real credential can never live in one. Rather than requiring an
operator to remember to run `ALTER ROLE ledger_app WITH PASSWORD ...`
by hand in every environment (which is exactly what bit us the first
time this service ran against a fresh database), `cmd/ledger-service`
sets that password itself on every startup, from
`LEDGER_APP_DB_PASSWORD`, right after applying migrations —
see `internal/db.ProvisionAppRolePassword`. The credential still only
ever comes from the environment, never from a `.sql` file; the only
thing that changed is *who* runs the `ALTER ROLE`.

## Database roles

Two distinct Postgres roles are involved, and the service must only
ever use one of them:

| Role | Used by | Privileges |
|---|---|---|
| admin/owner (e.g. the default `postgres` superuser locally) | running migrations only | full DDL: create/alter/drop tables, functions, triggers, roles |
| `ledger_app` (created in migration `000002_restrict_app_role`) | the running service, at all times | `SELECT`/`INSERT`/`UPDATE` on `accounts`; `SELECT`/`INSERT` only on `transactions` and `ledger_entries` — no `UPDATE`, no `DELETE` anywhere, no DDL |

The service connecting as `ledger_app` instead of a superuser is a
deliberate defense-in-depth measure: even if application code has a bug,
or is compromised via a SQL-injection-style vector, the database itself
refuses to let it rewrite or delete posted ledger history. This backs
up the "never delete or mutate a posted ledger entry" rule in
`CLAUDE.md` at the privilege level, not just in code review.

### Setting `ledger_app`'s password

Handled automatically — see "First-time setup" above. The service runs
`ALTER ROLE ledger_app WITH PASSWORD ...` itself on every startup,
using the same admin/owner connection `RunMigrations` already needs,
sourced from `LEDGER_APP_DB_PASSWORD`. This is idempotent, so it's just
as safe to run on the 100th startup as the first.

- **Local dev / docker-compose:** set `LEDGER_APP_DB_PASSWORD` and
  `LEDGER_MIGRATE_DB_PASSWORD` in the environment (see the root
  `docker-compose.yml`) — no separate step required.
- **Production (k3s):** store both passwords in a Kubernetes `Secret`
  and inject them as environment variables on the Pod. Never put them
  in a ConfigMap or a manifest checked into git.

### Environment variables

Database name and SSL mode are shared by both roles; host, port, and
credentials can all differ. All are read by `internal/db.LoadConfig()`.

| Variable | Default | Notes |
|---|---|---|
| `LEDGER_DB_HOST` | `localhost` | Where the runtime pool (`AppDSN`) connects. In docker-compose, this is PgBouncer, not Postgres directly — see "PgBouncer" below. |
| `LEDGER_DB_PORT` | `5432` | |
| `LEDGER_DB_NAME` | `ledgerly` | |
| `LEDGER_DB_SSLMODE` | `disable` | set to `require` (or stricter) in production |
| `LEDGER_APP_DB_USER` | `ledger_app` | the restricted runtime role |
| `LEDGER_APP_DB_PASSWORD` | *(required, no default)* | the service sets this as `ledger_app`'s Postgres password on every startup — see "First-time setup" |
| `LEDGER_MIGRATE_DB_HOST` | same as `LEDGER_DB_HOST` | Where migrations (`MigrateURL`) connect -- **always Postgres directly, never PgBouncer**, regardless of what `LEDGER_DB_HOST` is set to. See "PgBouncer" below for why this can't share `LEDGER_DB_HOST`'s value once PgBouncer is in the picture. |
| `LEDGER_MIGRATE_DB_PORT` | same as `LEDGER_DB_PORT` | |
| `LEDGER_MIGRATE_DB_USER` | `postgres` | the admin/owner role used only for migrations |
| `LEDGER_MIGRATE_DB_PASSWORD` | *(required, no default)* | |

Startup fails immediately with a clear error if either password is
missing, rather than attempting to connect with an empty credential.
The two host/port pairs defaulting to each other's values means a
single-Postgres setup with no PgBouncer (the default above) needs zero
extra configuration; only an environment that actually adds PgBouncer
needs to point `LEDGER_MIGRATE_DB_HOST`/`PORT` at Postgres directly
while `LEDGER_DB_HOST`/`PORT` point at PgBouncer instead.

## PgBouncer

`docker-compose.yml` puts [PgBouncer](https://www.pgbouncer.org/) in
front of Postgres, in **transaction pooling** mode, and points
`LEDGER_DB_HOST`/`LEDGER_DB_PORT` (i.e. `AppDSN`, the runtime pool
every request handler queries through) at it instead of at Postgres
directly. This is what actually makes running multiple `ledger-service`
replicas (`docker-compose up --scale ledger-service=3`, see the root
README's "Prove it scales yourself") safe on Postgres's connection
budget: each replica already runs its own client-side pool
(`*pgxpool.Pool`), so N replicas × M pool connections each would open
N×M real Postgres backend connections without PgBouncer in between --
transaction pooling lets PgBouncer multiplex all of that down to a
small, fixed number of actual Postgres connections, handing one out per
transaction rather than per client connection.

Two things had to change to make this safe, both non-obvious enough to
be worth calling out explicitly (each is also documented at its own
call site):

- **Migrations bypass PgBouncer entirely** (`LEDGER_MIGRATE_DB_HOST`/
  `PORT`, above) -- see `MigrateURL`'s doc comment in
  `internal/db/config.go`. golang-migrate serializes concurrent
  migration attempts from multiple simultaneously-starting replicas
  using a Postgres advisory lock, which is scoped to one *session*.
  Transaction pooling can hand a session's next transaction to a
  *different* backend connection than the one holding the lock,
  silently breaking that serialization -- two replicas could then race
  to apply the same migration concurrently. `internal/db.ProvisionAppRolePassword`'s
  admin connection bypasses PgBouncer for the same reason.
- **The runtime pool forces `pgx.QueryExecModeExec`** -- see
  `Connect`'s doc comment in `internal/db/pool.go`. pgx's default query
  mode caches server-side prepared statements per connection, assuming
  the same backend connection will still have them next time; under
  transaction pooling, PgBouncer can hand the pool's next transaction
  to a backend that never saw that `PREPARE`, which surfaces as
  "prepared statement does not exist" errors under concurrent load.
  `QueryExecModeExec` never names or reuses a statement across round
  trips, so it works identically whether `AppDSN` points at PgBouncer
  or straight at Postgres.

Local dev without `docker-compose` (e.g. `go run ./cmd/ledger-service`
against your own Postgres) is unaffected either way: PgBouncer is
opt-in infrastructure this service degrades to not having, not a hard
dependency -- point `LEDGER_DB_HOST` at Postgres directly and both of
the above remain correct (harmless) no-ops.

### How PgBouncer itself is configured

The `pgbouncer/pgbouncer` image takes no config file -- its entrypoint
generates `/etc/pgbouncer/pgbouncer.ini` itself from `DATABASES_*`/
`PGBOUNCER_*` environment variables on every start (confirmed by
extracting and reading that entrypoint script directly from the pulled
image, not assumed from documentation). `docker-compose.yml` sets
`DATABASES_HOST`/`PORT`/`DBNAME`/`USER`/`PASSWORD` to `ledger_app`'s
own credentials and leaves the client-facing database name as the
image's default wildcard (`*`), so any dbname a client requests proxies
to that one upstream. `PGBOUNCER_AUTH_TYPE=any` means PgBouncer
performs no authentication of its own on incoming connections and
always authenticates upstream using those same `DATABASES_USER`/
`PASSWORD` credentials regardless of what a client presents -- a
deliberate local-dev simplification, safe here since PgBouncer is never
published to the host and only `ledger-service`, on the internal
Compose network, ever talks to it. See `docker-compose.yml`'s own
comment on the `pgbouncer` service for the full reasoning.

### How the two roles stay separate in code, not just in the database

`internal/db.Config` exposes exactly two ways to turn its fields into a
connection string: `AppDSN()` and `MigrateURL()`. There is no path that
lets code build a string mixing the admin user (or its direct-to-Postgres
host/port) with the app password (or its possibly-PgBouncer host/port)
or vice versa — each method only ever reads its own set of fields.

- `internal/db/migrate.go`'s `RunMigrations` is the *only* caller of
  `MigrateURL()`. It opens a connection, applies pending `.sql` files
  from the embedded `migrations/` directory (in order, tracked by
  golang-migrate's own `schema_migrations` table), and closes that
  connection before returning. The admin credential is never held
  anywhere else in the process.
- `internal/db/pool.go`'s `Connect` is the *only* caller of `AppDSN()`.
  It opens the `*pgxpool.Pool` that every request handler queries
  through for the rest of the process's life.
- `cmd/ledger-service/main.go` calls `RunMigrations` first, then
  `Connect` — in that order, every startup, unconditionally. There's no
  code path where the pool used to serve HTTP requests was built from
  anything but `AppDSN()`.

### Running the service

```bash
go run ./cmd/ledger-service
```

On every startup the service: 1) applies any pending migrations using
the admin role, 2) sets `ledger_app`'s password from
`LEDGER_APP_DB_PASSWORD` using that same admin connection (see
"First-time setup" above), then 3) opens its runtime pool as
`ledger_app` and pings it before serving traffic. `GET /health`
reflects that pool's real status (`200` if Postgres answers, `503` if
it doesn't) — so a load balancer or orchestrator can tell a replica
that's lost its database connection from one that's actually healthy.
Re-running migrations (and re-setting the password) against an
already-provisioned database is a safe no-op, so this works
unchanged on every subsequent restart, not just the first.

## API

All request and response bodies are JSON. Money amounts are always
integer minor units (cents) — never floats, to avoid rounding error in
a ledger. `handlers` (`internal/handler`) do request parsing, path/body
validation, and error-to-status-code mapping only; all business logic
lives in `internal/ledger` (see its doc comments, especially
`PostTransaction`'s, for *why* each rule exists).

### `POST /accounts` — create an account

```bash
curl -X POST localhost:8080/accounts \
  -d '{"name": "Alice'\''s Wallet", "account_type": "wallet"}'
```

`currency` is optional (defaults to `"USD"`). `account_type` is
`"wallet"` or `"system"` — almost everything wallet-service creates
will be `"wallet"`; `"system"` is for ledger-internal contra-accounts
like the seeded external funding account.

Response `201 Created`:

```json
{
  "id": "5b2e...",
  "name": "Alice's Wallet",
  "account_type": "wallet",
  "currency": "USD",
  "balance": 0,
  "version": 0,
  "created_at": "2026-09-08T09:00:00Z",
  "updated_at": "2026-09-08T09:00:00Z"
}
```

### `GET /accounts` — list every wallet account

```bash
curl localhost:8080/accounts
```

Response `200 OK`:

```json
[
  {
    "id": "5b2e...",
    "name": "Alice's Wallet",
    "account_type": "wallet",
    "currency": "USD",
    "balance": 1500,
    "version": 2,
    "created_at": "2026-09-08T09:00:00Z",
    "updated_at": "2026-09-08T09:00:03Z"
  }
]
```

Oldest-created first; `[]` (never `null`) when there are none yet. This
unconditionally filters to `account_type = "wallet"` — system accounts
(currently just the seeded external funding account) are always
excluded, with no query parameter to opt back in. Nothing that creates
an account through this API (`POST /accounts` here, or wallet-service's
`POST /wallets`) ever produces a `"wallet"` account a real user
shouldn't see, but the reverse isn't true: the external funding account
exists purely to make top-ups balance under double-entry accounting
(see `ExternalFundingAccountID`'s doc comment in
`internal/ledger/types.go`), nothing ever treats it as a wallet, and
listing it here would just confuse a caller into thinking it's a real,
selectable wallet. A caller that genuinely needs every account
regardless of type already has `GET /integrity` below for that; no
current caller needs an unfiltered or system-only listing, so this
endpoint doesn't grow a parameter for a distinction nothing yet uses.

### `GET /accounts/{id}/balance` — current balance

```bash
curl localhost:8080/accounts/5b2e.../balance
```

Response `200 OK`:

```json
{ "account_id": "5b2e...", "balance": 1500 }
```

This is the cached `accounts.balance` column — an O(1) read, not a
recomputation from history. See `/accounts/{id}/integrity` below for
the value recomputed from scratch.

### `GET /accounts/{id}/history` — ledger entries, chronological order

```bash
curl localhost:8080/accounts/5b2e.../history
```

Response `200 OK`:

```json
{
  "account_id": "5b2e...",
  "entries": [
    {
      "id": "9f1c...",
      "transaction_id": "a001...",
      "transaction_type": "top_up",
      "description": "seed",
      "direction": "credit",
      "amount": 1500,
      "created_at": "2026-09-08T09:00:01Z"
    }
  ]
}
```

Oldest entry first. An account that exists but has no entries yet
returns `200` with `"entries": []`; an account that doesn't exist
returns `404` (see the error table below) — the handler queries history
first (one round trip for the common case) and only calls
`ledger.AccountExists` to tell the two apart when that query comes back
empty.

### `POST /transactions` — post a top-up or transfer

```bash
curl -X POST localhost:8080/transactions \
  -d '{
    "idempotency_key": "client-generated-uuid",
    "transaction_type": "transfer",
    "description": "optional",
    "legs": [
      {"account_id": "<source>",      "direction": "debit",  "amount": 500},
      {"account_id": "<destination>", "direction": "credit", "amount": 500}
    ]
  }'
```

Legs must balance (debits = credits) and there must be at least two —
`internal/ledger` checks this before touching the database. A top-up's
legs are `{debit: ExternalFundingAccountID, credit: <wallet>}`; a
transfer's are `{debit: <source wallet>, credit: <destination wallet>}`.

Response `201 Created` for a newly-posted transaction, `200 OK` if
`idempotency_key` had already been processed (`"replayed": true` either
way tells you which happened — see `internal/ledger.PostTransaction`'s
doc comment for exactly what replay does and does not verify):

```json
{
  "transaction": {
    "id": "a001...",
    "idempotency_key": "client-generated-uuid",
    "transaction_type": "transfer",
    "description": "optional",
    "created_at": "2026-09-08T09:00:02Z"
  },
  "entries": [
    { "id": "...", "transaction_id": "a001...", "account_id": "<source>",      "direction": "debit",  "amount": 500, "created_at": "..." },
    { "id": "...", "transaction_id": "a001...", "account_id": "<destination>", "direction": "credit", "amount": 500, "created_at": "..." }
  ],
  "replayed": false
}
```

### `GET /accounts/{id}/integrity` — one account's drift check

```bash
curl localhost:8080/accounts/5b2e.../integrity
```

Response `200 OK`:

```json
{
  "account_id": "5b2e...",
  "cached_balance": 1500,
  "computed_balance": 1500,
  "drifted": false
}
```

`computed_balance` is summed fresh from `ledger_entries`;
`cached_balance` is the same value `GET .../balance` returns.
`drifted: true` would mean the cache and the ledger's actual history
disagree — expected to never happen in normal operation (see the doc
comment on `ledger.IntegrityResult.Drifted`).

### `GET /integrity` — every account's drift check

```bash
curl localhost:8080/integrity
```

Response `200 OK`:

```json
{
  "results": [
    { "account_id": "...", "cached_balance": 1500, "computed_balance": 1500, "drifted": false },
    { "account_id": "...", "cached_balance": -1500, "computed_balance": -1500, "drifted": false }
  ],
  "drifted": false
}
```

`drifted` at the top level is `true` if *any* account in `results`
drifted — this is what a live "everything still reconciles" widget on
the frontend would poll.

### Error responses

Every error response is `{"error": "<message>"}`. `internal/handler`
maps `internal/ledger`'s sentinel errors to status codes as follows
(anything unrecognized becomes a `500` with the real error logged
server-side only, never echoed to the client):

| Status | When |
|---|---|
| `400 Bad Request` | Malformed JSON body; a path `{id}` that isn't a valid UUID; `ledger.ErrInvalidParams` (e.g. unrecognized `account_type`/`transaction_type`); `ledger.ErrInvalidLegs` (fewer than two legs, a non-positive amount, an unrecognized `direction`, or debits ≠ credits) |
| `404 Not Found` | `ledger.ErrAccountNotFound` — an account referenced by the URL or by a leg doesn't exist |
| `409 Conflict` | `ledger.ErrInsufficientFunds` — the request is well-formed, but applying it would drive a wallet negative |
| `503 Service Unavailable` | `ledger.ErrMaxRetriesExceeded` — the optimistic-concurrency retry loop in `PostTransaction` ran out of attempts on one of this transaction's accounts; transient, safe to retry |
| `500 Internal Server Error` | Anything else (a real database/connectivity problem) |

## Events (RabbitMQ)

Every genuinely new (non-replayed) `POST /transactions` commit publishes
a `transaction.posted` event to RabbitMQ, for `notification-service`'s
live activity feed — see its README for the consumer side and the
full event shape.

### Delivery guarantee: best-effort, not exactly-once

This is deliberately **not** a transactional outbox. An outbox would
write the event to a table in the *same* Postgres transaction as the
ledger entries, then a separate relay process would publish it with
retries until it succeeds — guaranteeing the event is eventually
published even across a crash. Instead, `internal/ledger.PostTransaction`
publishes synchronously, best-effort, right after its Postgres
transaction has already committed: on success, RabbitMQ gets the
event; if publishing fails (RabbitMQ unreachable, connection dropped
mid-flight, whatever), the failure is logged and swallowed —
**the request still succeeds**, and the write it describes is already
durable in Postgres regardless.

This tradeoff is a direct consequence of what `notification-service`
is for: a live activity feed is a nice-to-have view onto state Postgres
already owns authoritatively (`GET /accounts/{id}/history`, `GET
/accounts/{id}/balance`, and `/integrity` are always the real answer),
not a system anything else depends on for correctness. Building outbox
machinery — an extra table, a relay process, dedup on the consumer
side — to protect a view nobody relies on for correctness would be
exactly the kind of over-engineering `CLAUDE.md`'s "smallest tool that
fits current scope" guidance warns against. See
`internal/ledger/events.go`'s doc comment for the full reasoning, and
the root README's Scaling Roadmap for what would justify revisiting
this.

The one thing this guarantees: a dropped event means the live feed
missed one update, and *only* that — never a lost, duplicated, or
corrupted ledger entry. Concretely, the only way an event is dropped is
a crash or RabbitMQ outage in the narrow window between this
transaction's commit and the publish call a few lines later; anything
that fails *before* commit (including RabbitMQ being unreachable for
the entire request) never touches the ledger at all.

### Environment variables

| Variable | Default | Notes |
|---|---|---|
| `LEDGER_RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | Full AMQP URI. Unlike the Postgres passwords above, this is never required — see `internal/events.Config`. |

### Topology

`internal/events.Publisher` declares (idempotently — safe regardless of
which service starts first) a durable topic exchange, `ledger.events`,
and publishes each event under the routing key `transaction.posted`.
It does not declare a queue: that's `notification-service`'s job, and
deliberately so — see its README for why (a queue per consumer, not one
shared queue, is what makes this a broadcast rather than a work queue).
