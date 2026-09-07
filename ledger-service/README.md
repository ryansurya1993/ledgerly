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
