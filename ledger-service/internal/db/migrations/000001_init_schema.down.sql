DROP TRIGGER IF EXISTS trg_ledger_entries_balanced ON ledger_entries;
DROP FUNCTION IF EXISTS check_transaction_balanced();
DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS transactions;
DROP TABLE IF EXISTS accounts;
-- pgcrypto is left installed; other migrations/schemas may depend on it.
