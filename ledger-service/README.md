# ledger-service

Core double-entry ledger: accounts, transactions, balance calculation,
integrity checks. Owns all financial state in Postgres. See the root
`README.md` and `CLAUDE.md` for the overall Ledgerly architecture.

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

Migration `000002_restrict_app_role.up.sql` creates the role with no
password — a migration file is version-controlled and readable by
anyone with repo access, so a real credential can never live in it.
Until a password is set, `ledger_app` cannot authenticate at all, which
is intentional: it means no environment can be left with a guessable
default credential.

Set it once per environment, from a secret that is never committed:

```sql
ALTER ROLE ledger_app WITH PASSWORD '<value from your secret store>';
```

- **Local dev (docker-compose):** run the above via `psql` against the
  local Postgres container after migrations apply, using a value from
  your `.env` file (which is gitignored).
- **Production (k3s):** store the password in a Kubernetes `Secret` and
  either run the `ALTER ROLE` as a one-off job when provisioning the
  database, or template it into an init job. Never put it in a
  ConfigMap or a manifest checked into git.

### Environment variables

Host, port, database name, and SSL mode are shared by both roles; only
the credentials differ. All are read by `internal/db.LoadConfig()`.

| Variable | Default | Notes |
|---|---|---|
| `LEDGER_DB_HOST` | `localhost` | |
| `LEDGER_DB_PORT` | `5432` | |
| `LEDGER_DB_NAME` | `ledgerly` | |
| `LEDGER_DB_SSLMODE` | `disable` | set to `require` (or stricter) in production |
| `LEDGER_APP_DB_USER` | `ledger_app` | the restricted runtime role |
| `LEDGER_APP_DB_PASSWORD` | *(required, no default)* | set via `ALTER ROLE` above |
| `LEDGER_MIGRATE_DB_USER` | `postgres` | the admin/owner role used only for migrations |
| `LEDGER_MIGRATE_DB_PASSWORD` | *(required, no default)* | |

Startup fails immediately with a clear error if either password is
missing, rather than attempting to connect with an empty credential.

### How the two roles stay separate in code, not just in the database

`internal/db.Config` exposes exactly two ways to turn its fields into a
connection string: `AppDSN()` and `MigrateURL()`. There is no path that
lets code build a string mixing the admin user with the app password or
vice versa — each method only ever reads its own pair of fields.

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
the admin role, then 2) opens its runtime pool as `ledger_app` and pings
it before serving traffic. `GET /health` reflects that pool's real
status (`200` if Postgres answers, `503` if it doesn't) — so a load
balancer or orchestrator can tell a replica that's lost its database
connection from one that's actually healthy.

**First boot against a brand-new database:** migrations create the
`ledger_app` role with no password (see above), so step 2 will fail
authentication the very first time, by design — nothing can serve
traffic with a guessable default credential. Set the password once
(`ALTER ROLE ledger_app WITH PASSWORD '...'`) and start the service
again; every run after that succeeds, and re-running migrations against
an already-migrated database is a safe no-op.

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
