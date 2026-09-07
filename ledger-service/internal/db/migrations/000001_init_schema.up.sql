-- Core double-entry schema for ledger-service: accounts, transactions,
-- and the ledger_entries that make up each transaction's debit/credit legs.

CREATE EXTENSION IF NOT EXISTS pgcrypto; -- gen_random_uuid()

-- ACCOUNTS
--
-- 'wallet' accounts are user-facing balances the operator owes back to
-- the user (a liability in accounting terms: credit increases it, debit
-- decreases it). 'system' accounts are internal contra-accounts needed to
-- keep every transaction balanced when money crosses the ledger's
-- boundary (e.g. a top-up has to come from somewhere on the ledger, even
-- though the money is "external") -- see the seed row below.
--
-- `balance` is a cached, denormalized value for O(1) reads. It is NOT the
-- source of truth -- ledger_entries is. It is only ever updated in the
-- same DB transaction that inserts the entries which justify the change,
-- guarded by `version` (optimistic concurrency: callers do
-- `UPDATE accounts SET balance = balance + $delta, version = version + 1
-- WHERE id = $id AND version = $expected` and retry on zero rows
-- affected). The integrity-check endpoint recomputes balances from
-- ledger_entries and compares them against this cache to catch drift.
CREATE TABLE accounts (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT NOT NULL,
    account_type TEXT NOT NULL CHECK (account_type IN ('wallet', 'system')),
    currency     CHAR(3) NOT NULL DEFAULT 'USD',
    balance      BIGINT NOT NULL DEFAULT 0, -- minor units (cents); cached, see above
    version      BIGINT NOT NULL DEFAULT 0, -- optimistic concurrency token
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Overdraft protection lives here, not just in application code, so it
    -- holds even if posting logic has a bug. System accounts are exempt:
    -- the external funding account's balance is expected to go negative
    -- (see seed row) since nothing outside the ledger balances it.
    CONSTRAINT accounts_wallet_balance_non_negative
        CHECK (account_type <> 'wallet' OR balance >= 0)
);

-- A fixed, well-known system account representing money entering the
-- ledger from outside (top-ups). Application code references this UUID
-- as a constant. Without it, a top-up would have nowhere to put its debit
-- leg and couldn't be modeled as a real double-entry transaction.
INSERT INTO accounts (id, name, account_type, currency, balance, version)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'External Funding Source',
    'system',
    'USD',
    0,
    0
);

-- TRANSACTIONS
--
-- One row per logical operation (a top-up, a transfer). This is the unit
-- idempotency is keyed on; the actual balance-affecting rows live in
-- ledger_entries below. Transactions are append-only: nothing here is
-- ever updated after insert -- corrections are modeled as a new
-- transaction with reversing entries, never a mutation of this row.
CREATE TABLE transactions (
    id   UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- The final, unbreakable guarantee against duplicate processing.
    -- wallet-service dedupes fast via Redis first (see CLAUDE.md), but
    -- Redis is a cache, not a durability guarantee -- a failover, or a
    -- race between two concurrent requests both checking Redis before
    -- either has written its key, could let a duplicate through. This
    -- UNIQUE constraint is where the correctness guarantee actually lives.
    idempotency_key TEXT NOT NULL UNIQUE,

    transaction_type TEXT NOT NULL CHECK (transaction_type IN ('top_up', 'transfer')),
    description       TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- LEDGER_ENTRIES
--
-- The actual debit/credit legs. Every transaction must have at least two
-- entries (one debit, one credit) whose amounts are equal -- enforced by
-- the deferred constraint trigger below rather than trusted to
-- application code alone.
CREATE TABLE ledger_entries (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id UUID NOT NULL REFERENCES transactions(id) ON DELETE RESTRICT,
    account_id     UUID NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    direction      TEXT NOT NULL CHECK (direction IN ('debit', 'credit')),
    amount         BIGINT NOT NULL CHECK (amount > 0), -- minor units; direction carries the sign
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ledger_entries_transaction_id ON ledger_entries (transaction_id);
CREATE INDEX idx_ledger_entries_account_history ON ledger_entries (account_id, created_at DESC);

-- Enforce "sum of debits = sum of credits" per transaction at the
-- database level, not just in application code. DEFERRABLE INITIALLY
-- DEFERRED means this runs at COMMIT time, once every entry for the
-- surrounding DB transaction has landed -- so a transfer's two INSERTs
-- (or a top-up's two) can happen in any order, or as separate statements,
-- without the trigger firing against a transiently-unbalanced state.
CREATE FUNCTION check_transaction_balanced() RETURNS TRIGGER AS $$
DECLARE
    imbalance BIGINT;
BEGIN
    SELECT COALESCE(SUM(CASE WHEN direction = 'debit' THEN amount ELSE -amount END), 0)
    INTO imbalance
    FROM ledger_entries
    WHERE transaction_id = NEW.transaction_id;

    IF imbalance <> 0 THEN
        RAISE EXCEPTION 'transaction % is not balanced: debits minus credits = %',
            NEW.transaction_id, imbalance;
    END IF;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER trg_ledger_entries_balanced
    AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    EXECUTE FUNCTION check_transaction_balanced();
