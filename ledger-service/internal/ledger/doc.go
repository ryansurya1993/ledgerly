// Package ledger implements ledger-service's core double-entry business
// logic: creating accounts, posting balanced debit/credit transactions,
// reading balances and history, and verifying that a cached balance
// still matches what the ledger_entries actually add up to.
//
// This package knows nothing about HTTP -- internal/handler calls into
// it. That separation keeps the concurrency-critical logic (see
// post_transaction.go) testable without spinning up a server, and keeps
// handlers thin.
package ledger
