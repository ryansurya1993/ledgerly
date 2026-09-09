# wallet-service

The visitor-facing API. Wraps ledger-service's lower-level double-entry
operations (accounts, legs, transactions) into wallet-shaped actions
(create a wallet, top up, transfer, view balance/history), and owns the
fast-path idempotency cache. See the root `README.md` and `CLAUDE.md`
for the overall Ledgerly architecture.

**wallet-service never touches Postgres directly.** All ledger state
lives behind ledger-service's HTTP API — see `internal/ledgerclient`,
the only thing in this service that calls it.

## How this fits together

```
browser (demo frontend) → wallet-service → ledger-service → Postgres
                                ↕
                              Redis (fast-path idempotency cache only)
```

- `internal/ledgerclient` — a thin HTTP client for ledger-service's API.
  Defines its own copy of ledger-service's request/response shapes
  rather than importing them, since the two are separate Go modules
  (independent dependency sets, independent deploys — see
  `ledger-service/README.md`'s equivalent note and
  `PROJECT_BRIEF.md`'s reasoning for per-service modules).
- `internal/idempotency` — the Redis-backed fast-path cache (see
  "Idempotency" below).
- `internal/handler` — HTTP handlers: parse/validate the wallet-shaped
  request, build the ledger-service call, map the response (and any
  error) back into a wallet-shaped one.

This is also what serves the demo frontend itself (plain static files,
`FRONTEND_DIR` below) — see the root README's "Frontend" section for
why the visitor-facing gateway is what hosts the visitor-facing page,
not a separate static file server.

## Idempotency: fast path vs. correctness guarantee

`POST /wallets/{id}/topup` and `POST /wallets/{id}/transfer` both take a
client-supplied `idempotency_key`. Two layers are involved, and only one
of them is the actual correctness guarantee:

1. **Fast path (this service, Redis):** before calling ledger-service,
   `internal/idempotency.Store` checks Redis for the key. A hit answers
   immediately — no call to ledger-service at all — by replaying the
   original successful response, except for one field: `replayed` is
   always forced to `true` (and the status to `200`), regardless of what
   the original response said. The original call's own response
   necessarily said `replayed: false` with `201` — that's what's true
   about *any* brand-new transaction's first response — and serving that
   byte-for-byte on every later hit would keep telling every caller
   after the first one the opposite of what's now true (see
   `internal/handler.writeReplayedCacheHit`'s doc comment). On success,
   the (real, `replayed: false`) response is cached under that key for
   `WALLET_IDEMPOTENCY_TTL_HOURS` (default 24h).
2. **Correctness guarantee (ledger-service, Postgres):**
   `transactions.idempotency_key` has a `UNIQUE` constraint in
   ledger-service's schema. This is what actually prevents a duplicate
   top-up or transfer from being applied twice — not Redis.

The fast path is explicitly *only* a latency optimization. If Redis is
unreachable, evicts a key before its TTL, or two concurrent requests
both miss the cache at the same instant and both call ledger-service,
the request still ends up correct: ledger-service's UNIQUE constraint
rejects the second insert and returns the original transaction (with
`"replayed": true`) instead. Every fast-path failure mode degrades to
"call ledger-service anyway" — never to "fail the request" — see the
doc comments on `internal/idempotency` and
`internal/handler.postTransaction` for exactly where that happens.

One more deliberate rule: **a failed or transient response from
ledger-service is never cached** — only a successful (2xx) one. If
ledger-service returns `503` (concurrency contention) or `409`
(insufficient funds), that must stay retryable under the same
idempotency key, not get permanently frozen into the cache as if it
were the final answer.

## Running the service

```bash
go run ./cmd/wallet-service
```

Requires ledger-service to already be running and migrated (see
`ledger-service/README.md`). Redis is optional at startup — see
"Environment variables" below.

### Environment variables

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `8081` | Deliberately different from ledger-service's default `8080`, so both can run locally at once without a port collision. |
| `LEDGER_SERVICE_URL` | `http://localhost:8080` | Base URL of ledger-service's API. |
| `WALLET_REDIS_ADDR` | `localhost:6379` | `host:port` of the fast-path idempotency Redis. |
| `WALLET_REDIS_PASSWORD` | *(empty)* | No default auth for local dev; set in production via a `Secret`, never a `ConfigMap` or a committed file. |
| `WALLET_REDIS_DB` | `0` | Redis logical DB index. |
| `WALLET_IDEMPOTENCY_TTL_HOURS` | `24` | How long a cached top-up/transfer response is kept. |
| `FRONTEND_DIR` | `../frontend` | Where the static demo frontend's files live — see `cmd/wallet-service/main.go` and the root README's "Frontend" section. The default resolves correctly for `go run ./cmd/wallet-service` from this directory; `docker-compose.yml` overrides it to `/frontend`, where it bind-mounts the repo's `frontend/` directory. |

Unlike `ledger-service/internal/db.LoadConfig` (which fails startup
immediately if its DB passwords are missing, because Postgres is a hard
dependency it cannot function without), **nothing here is required, and
this service never pings Redis at startup.** Redis is an optional fast
path — a wallet-service replica that can't reach Redis at all still
serves every request correctly, just without the caching optimization.
Failing startup over that would be the wrong call; see
`internal/idempotency`'s package doc and `internal/handler.Health`'s
doc comment for the full reasoning.

## API

All request and response bodies are JSON. Money amounts are integer
minor units (cents), same as ledger-service.

### `POST /wallets` — create a wallet

```bash
curl -X POST localhost:8081/wallets -d '{"name": "Alice'\''s Wallet"}'
```

`currency` is optional (defaults to `"USD"`, same as ledger-service).
Internally this calls ledger-service's `POST /accounts` with
`account_type: "wallet"` — a wallet-service visitor never creates a
`"system"` account (that's ledger-internal, e.g. the external funding
source).

Response `201 Created`:

```json
{
  "id": "5b2e...",
  "name": "Alice's Wallet",
  "currency": "USD",
  "balance": 0,
  "created_at": "2026-09-08T09:00:00Z",
  "updated_at": "2026-09-08T09:00:00Z"
}
```

Note this omits ledger-service's `account_type` and `version` fields —
both are ledger implementation details (the latter is the optimistic
concurrency token), not part of a wallet-shaped API.

### `GET /wallets` — list every wallet

```bash
curl localhost:8081/wallets
```

Response `200 OK`:

```json
[
  {
    "id": "5b2e...",
    "name": "Alice's Wallet",
    "currency": "USD",
    "balance": 1500,
    "created_at": "2026-09-08T09:00:00Z",
    "updated_at": "2026-09-08T09:00:03Z"
  }
]
```

A passthrough to ledger-service's `GET /accounts`, which already excludes
system accounts like the external funding source (see its README) — so
every entry here is a real, selectable wallet. `[]` (never `null`) when
none exist yet. Same convention as `POST /wallets` above: `account_type`
and `version` are omitted, since they're ledger implementation details,
not part of a wallet-shaped API.

### `POST /wallets/{id}/topup` — fund a wallet

```bash
curl -X POST localhost:8081/wallets/5b2e.../topup \
  -d '{"idempotency_key": "client-generated-uuid", "amount": 1000, "description": "optional"}'
```

Builds the two-leg transaction ledger-service needs — `{debit:
ExternalFundingAccountID, credit: <this wallet>}` — so the caller never
constructs raw ledger legs.

Response `201 Created` for a fresh top-up, `200 OK` if `idempotency_key`
had already been used (either via the Redis fast path or ledger-service
itself reporting a replay — see "Idempotency" above):

```json
{
  "transaction_id": "a001...",
  "wallet_id": "5b2e...",
  "amount": 1000,
  "idempotency_key": "client-generated-uuid",
  "description": "optional",
  "created_at": "2026-09-08T09:00:02Z",
  "replayed": false
}
```

### `POST /wallets/{id}/transfer` — move funds between two wallets

```bash
curl -X POST localhost:8081/wallets/5b2e.../transfer \
  -d '{"idempotency_key": "client-generated-uuid", "destination_wallet_id": "<other wallet>", "amount": 500}'
```

Builds `{debit: <source, from the URL>, credit: <destination_wallet_id>}`.
`destination_wallet_id` equal to the source `{id}` is rejected with
`400` before calling ledger-service — ledger-service's leg-netting would
otherwise silently turn a "transfer to yourself" into a no-op instead of
an error (see the doc comment on `internal/handler.Transfer`).

Response shape mirrors top-up, with both wallet IDs:

```json
{
  "transaction_id": "a002...",
  "source_wallet_id": "5b2e...",
  "destination_wallet_id": "9f1c...",
  "amount": 500,
  "idempotency_key": "client-generated-uuid",
  "created_at": "2026-09-08T09:00:03Z",
  "replayed": false
}
```

### `GET /wallets/{id}/balance`

```bash
curl localhost:8081/wallets/5b2e.../balance
```

```json
{ "wallet_id": "5b2e...", "balance": 1500 }
```

Passes straight through to ledger-service's
`GET /accounts/{id}/balance`.

### `GET /wallets/{id}/history`

```bash
curl localhost:8081/wallets/5b2e.../history
```

```json
{
  "wallet_id": "5b2e...",
  "entries": [
    {
      "id": "9f1c...",
      "transaction_id": "a001...",
      "transaction_type": "top_up",
      "description": "optional",
      "direction": "credit",
      "amount": 1000,
      "created_at": "2026-09-08T09:00:02Z"
    }
  ]
}
```

Passes straight through to ledger-service's
`GET /accounts/{id}/history`, including its documented behavior: an
existing-but-empty wallet returns `200` with `"entries": []`, a
nonexistent one returns `404`.

### `GET /integrity` — every account's drift check

```bash
curl localhost:8081/integrity
```

```json
{
  "results": [
    { "account_id": "...", "cached_balance": 1500, "computed_balance": 1500, "drifted": false }
  ],
  "drifted": false
}
```

The one endpoint in this service that isn't wallet-shaped: a direct
passthrough of ledger-service's `GET /integrity` (see its README),
field names included (`account_id`, not `wallet_id` — `results` can
include ledger-internal accounts, like the external funding account,
that aren't wallets). It exists here purely so the demo frontend never
needs to treat ledger-service as a third origin alongside
wallet-service and notification-service — see the root README's
"Frontend" section.

### Error responses

Every error response is `{"error": "<message>"}`.
`internal/handler.writeLedgerClientError` handles the mapping:

| Status | When |
|---|---|
| `400 Bad Request` | Malformed JSON body; a path `{id}` or `destination_wallet_id` that isn't a valid UUID; missing `idempotency_key`; non-positive `amount`; `destination_wallet_id` equal to the source wallet |
| *(passed through from ledger-service)* | Any status ledger-service itself returned for the underlying `/accounts` or `/transactions` call — `404` (wallet not found), `409` (insufficient funds), `503` (concurrency contention under load), etc. See `ledger-service/README.md`'s own error table for the full set. |
| `502 Bad Gateway` | ledger-service could not be reached at all (network failure, timeout, connection refused) |

## Running tests

```bash
go test ./...
```

`internal/ledgerclient`'s tests run against an in-process
`httptest.Server` standing in for ledger-service (no real ledger-service
needed). `internal/idempotency`'s tests run against a real, disposable
Redis container via Docker (same approach as
`ledger-service/internal/ledger`'s tests against a real Postgres —
skips gracefully if Docker isn't available). `internal/handler`'s tests
use scripted fakes for both dependencies and need neither.
