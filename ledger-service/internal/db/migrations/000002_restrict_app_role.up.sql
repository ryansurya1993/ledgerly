-- Least-privilege runtime role for ledger-service.
--
-- Migrations (this file included) run as a privileged owner/admin role
-- with full DDL rights. The service itself must NEVER connect as that
-- role, or as a Postgres superuser -- see ledger-service/README.md for
-- how its connection string is built and how this role's password is
-- provisioned.
--
-- This role can read and append ledger data but cannot alter schema,
-- drop anything, or rewrite history: no UPDATE/DELETE on transactions
-- or ledger_entries (append-only, per the "never delete or mutate a
-- posted ledger entry" rule in CLAUDE.md), and no DELETE anywhere --
-- accounts are only ever inserted or updated (balance/version), never
-- removed.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ledger_app') THEN
        CREATE ROLE ledger_app WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE;
    END IF;
END
$$;

-- Created with no password on purpose: this file is version-controlled
-- and readable by anyone with repo access, so a real credential can
-- never live in it. Until a password is set out-of-band (see the README
-- section this migration points to), ledger_app cannot authenticate at
-- all -- so no environment is left with a guessable default credential.

-- GRANT CONNECT ON DATABASE needs a literal identifier, and the local
-- vs. production database name may differ -- current_database() keeps
-- this migration correct either way.
DO $$
BEGIN
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO ledger_app', current_database());
END
$$;

GRANT USAGE ON SCHEMA public TO ledger_app;

GRANT SELECT, INSERT, UPDATE ON accounts TO ledger_app;
GRANT SELECT, INSERT ON transactions TO ledger_app;
GRANT SELECT, INSERT ON ledger_entries TO ledger_app;

-- Keeps this guarantee from silently eroding as the schema evolves: any
-- table a future migration creates (as long as it runs under the same
-- admin role as this one) starts ledger_app off at read+append only.
-- A table that genuinely needs UPDATE, like accounts above, still needs
-- that grant added explicitly in the migration that creates it.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT ON TABLES TO ledger_app;
