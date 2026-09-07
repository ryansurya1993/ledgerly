ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT ON TABLES FROM ledger_app;

DO $$
BEGIN
    EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM ledger_app', current_database());
END
$$;

-- DROP OWNED BY revokes every privilege granted to ledger_app in this
-- database, not just the ones this migration happens to know about --
-- so DROP ROLE below still succeeds even if a later migration granted
-- it access to something else (e.g. a new table picking up the default
-- privileges rule above). Without this, DROP ROLE fails with "cannot be
-- dropped because some objects depend on it".
DROP OWNED BY ledger_app;

DROP ROLE IF EXISTS ledger_app;
