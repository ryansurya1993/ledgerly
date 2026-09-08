package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// IntegrityResult compares one account's cached balance against what
// its ledger_entries actually add up to.
type IntegrityResult struct {
	AccountID uuid.UUID

	// CachedBalance is accounts.balance -- what GetBalance returns.
	CachedBalance int64

	// ComputedBalance is SUM(credits) - SUM(debits) over every
	// ledger_entries row for this account, recomputed from scratch.
	ComputedBalance int64
}

// Drifted reports whether the cache and the recomputed value disagree.
// In normal operation this is always false: every write to
// accounts.balance happens in the same Postgres transaction as the
// ledger_entries rows that justify it (see PostTransaction), so the two
// can only diverge if that invariant was broken -- a bug, a manual data
// fix that skipped this package, or corruption. That's exactly why this
// check exists as a standalone, independently-computed verification
// rather than trusting the cache it's checking.
func (r IntegrityResult) Drifted() bool {
	return r.CachedBalance != r.ComputedBalance
}

// CheckAccountIntegrity recomputes accountID's balance directly from
// ledger_entries and compares it to the cached accounts.balance value.
// It checks the account exists (via AccountExists) before opening a
// transaction to compute anything -- a nonexistent account should
// report ErrAccountNotFound, not a meaningless zeroed/empty result.
//
// The balance reads themselves run in one Postgres transaction at the
// default READ COMMITTED isolation level so they see a single
// consistent snapshot -- without that, a concurrent PostTransaction
// call landing between the two SELECTs could make an already-consistent
// account look drifted (e.g. the cached balance read reflects a
// transfer that the ledger_entries read, taken a moment later, already
// includes twice, or not yet). The transaction is read-only and rolled
// back, never committed -- it exists purely to pin a snapshot for these
// two reads, not to modify anything.
func (l *Ledger) CheckAccountIntegrity(ctx context.Context, accountID uuid.UUID) (IntegrityResult, error) {
	exists, err := l.AccountExists(ctx, accountID)
	if err != nil {
		return IntegrityResult{}, err
	}
	if !exists {
		return IntegrityResult{}, fmt.Errorf("%w: %s", ErrAccountNotFound, accountID)
	}

	tx, err := l.db.Begin(ctx)
	if err != nil {
		return IntegrityResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	result := IntegrityResult{AccountID: accountID}

	err = tx.QueryRow(ctx, `SELECT balance FROM accounts WHERE id = $1`, accountID).Scan(&result.CachedBalance)
	if errors.Is(err, pgx.ErrNoRows) {
		// The account existed a moment ago (AccountExists above) but is
		// gone by the time this transaction reads it. Accounts are
		// never deleted in this system (no DELETE grant -- see
		// migration 000002), so this should be unreachable in practice;
		// kept as a safety net rather than assumed away.
		return IntegrityResult{}, fmt.Errorf("%w: %s", ErrAccountNotFound, accountID)
	}
	if err != nil {
		return IntegrityResult{}, fmt.Errorf("read cached balance: %w", err)
	}

	err = tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN direction = 'credit' THEN amount ELSE -amount END), 0)
		FROM ledger_entries
		WHERE account_id = $1
	`, accountID).Scan(&result.ComputedBalance)
	if err != nil {
		return IntegrityResult{}, fmt.Errorf("compute balance from ledger entries: %w", err)
	}

	return result, nil
}

// CheckAllAccountsIntegrity runs CheckAccountIntegrity for every
// account, for the ledger-wide "everything still reconciles" view (the
// project brief's stress-test panel integrity widget). Each account is
// still checked with its own snapshot transaction, so this makes no
// claim about all accounts being consistent with each other at a single
// instant -- only that each one individually reconciles with its own
// history.
func (l *Ledger) CheckAllAccountsIntegrity(ctx context.Context) ([]IntegrityResult, error) {
	rows, err := l.db.Query(ctx, `SELECT id FROM accounts ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan account id: %w", err)
		}
		ids = append(ids, id)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return nil, fmt.Errorf("read account id rows: %w", rowsErr)
	}

	results := make([]IntegrityResult, 0, len(ids))
	for _, id := range ids {
		r, err := l.CheckAccountIntegrity(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("check integrity for account %s: %w", id, err)
		}
		results = append(results, r)
	}
	return results, nil
}
