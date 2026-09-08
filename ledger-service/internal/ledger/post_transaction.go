package ledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PostTransactionParams describes one logical operation to post: a
// top-up or a transfer, identified by an idempotency key, made up of
// two or more balanced debit/credit legs.
type PostTransactionParams struct {
	IdempotencyKey  string
	TransactionType TransactionType
	Description     string
	Legs            []Leg
}

// PostTransactionResult is what a successful PostTransaction call
// returns: the transaction row and the ledger entries it produced.
type PostTransactionResult struct {
	Transaction Transaction
	Entries     []Entry

	// Replayed is true if IdempotencyKey had already been processed by
	// an earlier call. In that case Transaction and Entries describe
	// the *original* posting -- this call did no new work and changed
	// no balances. Callers (wallet-service) can use this to tell "your
	// request just succeeded" apart from "your request had already
	// succeeded", which matters for logging/metrics even though the
	// caller-visible result (the transaction went through, exactly
	// once) is identical either way.
	Replayed bool
}

// Tuning constants for the optimistic-concurrency retry loop below.
// See the "Concurrency" doc comment on PostTransaction for what these
// are actually defending against.
const (
	maxPostAttempts  = 30
	retryBaseDelay   = 2 * time.Millisecond
	retryMaxDelay    = 40 * time.Millisecond
	retryMaxDoubling = 5 // 2ms * 2^5 = 64ms, already above retryMaxDelay
)

// PostTransaction is the core double-entry write: it inserts one
// transactions row and Legs' worth of ledger_entries rows, and applies
// each leg's effect to its account's cached balance -- all atomically,
// in a single Postgres transaction, or not at all.
//
// # Validation
//
// Legs must form a valid double-entry transaction: at least two legs,
// every amount strictly positive, and debits summing to exactly
// credits. This is checked in Go first so a malformed call fails fast
// with ErrInvalidLegs instead of reaching the database at all -- the
// database enforces the same balance rule too (the deferred constraint
// trigger in migration 000001), but only as a backstop; relying on it
// as the primary check would mean every mistake surfaces as an opaque
// Postgres trigger error instead of a typed one.
//
// # Idempotency
//
// IdempotencyKey is enforced by transactions.idempotency_key's UNIQUE
// constraint. wallet-service is expected to dedupe most repeats via
// Redis before ever calling this far (see CLAUDE.md), but that's a
// cache, not a durability guarantee -- this UNIQUE constraint is where
// the real guarantee lives. If the key already exists, PostTransaction
// does no new work and returns the original result with Replayed set,
// rather than erroring. Note that it does not verify the replayed
// call's legs match the original's -- a client that reuses a key with
// different legs gets the *original* transaction back silently. That's
// an accepted simplification for this project's scope (a single
// service, not a public multi-tenant API); a stricter implementation
// would store a hash of the request alongside the key and reject a
// mismatch.
//
// # Events
//
// A genuinely new (non-replayed) commit triggers a best-effort
// notification to l.publisher, for notification-service's live
// activity feed -- see events.go's doc comment for why this is
// fire-and-forget after commit rather than part of this transaction.
//
// # Concurrency: what happens when two transfers hit the same account
//
// accounts.balance is a cache (see migration 000001); accounts.version
// is what makes updating that cache safe under concurrent writers. The
// algorithm, per account touched by this transaction's legs:
//
//  1. SELECT the account's current version (a plain read, no lock).
//  2. UPDATE accounts SET balance = balance + delta, version = version + 1
//     WHERE id = $id AND version = $version_from_step_1.
//
// If nothing else touched that account between steps 1 and 2, the
// UPDATE matches one row and we move on. If another transaction
// committed a change to that account in between -- its version no
// longer matches what we read in step 1 -- the UPDATE matches zero
// rows. That's not an error from Postgres; it's the signal that we lost
// a race, and the whole attempt (not just that one UPDATE) is rolled
// back and retried from scratch via attemptPostTransaction. It has to
// be the *whole* attempt, not just the losing UPDATE, because this is
// one Postgres transaction: the transactions/ledger_entries rows this
// attempt already inserted are only valid alongside the balance updates
// that justify them, so a partial retry isn't an option -- roll back
// everything, start over.
//
// A concrete race, step by step, with two transfers both hitting
// account A concurrently:
//
//	Tx1 (transfer A->B)              Tx2 (transfer A->C)
//	SELECT version FROM A  -> 5      SELECT version FROM A -> 5
//	UPDATE A WHERE version=5         (blocks: Postgres queues Tx2's
//	  -> matches, A.version=6          UPDATE behind Tx1's row lock on A)
//	... inserts entries, COMMITS
//	                                  UPDATE A WHERE version=5 now runs
//	                                    -> A's *current* version is 6,
//	                                       not 5 -> matches zero rows
//	                                  Tx2 rolls back, retries the whole
//	                                    attempt: re-reads version=6,
//	                                    UPDATE WHERE version=6 succeeds
//
// Two things are easy to miss here. First, Postgres itself already
// guarantees Tx1 and Tx2's UPDATEs can't interleave and silently lose
// one side's change -- the second UPDATE physically blocks on the row
// lock until the first commits or rolls back. A naive
// "balance = balance + delta" UPDATE with no version check would
// therefore never lose an update either. What the version column adds
// is *visibility*: without it, Tx2 would simply block for however long
// Tx1's transaction takes (and does the same for every other concurrent
// writer queued behind it), with no way for the application to notice
// contention is happening or bound how long it waits. With it, Tx2's
// UPDATE returns immediately once unblocked, tells us in one round trip
// whether we won or lost the race (rows affected), and lets the retry
// loop below apply its own backoff policy instead of sitting inside an
// open transaction hoping Postgres's lock queue drains promptly.
//
// Second: a transaction with two legs (a transfer) touches two
// accounts, and updates to both happen inside the same Postgres
// transaction. If Tx1 is "transfer A->B" (locks A, then wants to lock
// B) and Tx2 is "transfer B->A" (locks B, then wants to lock A) running
// concurrently, each holds the lock the other is waiting for -- a
// classic deadlock, and Postgres will abort one of them with a
// deadlock_detected error rather than let both hang forever. That
// error isn't a version conflict our retry loop recognizes, so it would
// surface to the caller as a raw failure instead of a transparent
// retry. netDeltas + sortedAccountIDs below avoid this entirely: every
// attempt locks the accounts it touches in the same global order
// (sorted by account ID) regardless of which account is the "source"
// and which is the "destination", so A->B and B->A always acquire locks
// A-then-B. Two transactions can still contend for the same lock, but
// they can never form a cycle waiting on each other -- so this project
// relies on the retry loop for contention, and never expects to see a
// real deadlock from account updates.
//
// PostTransaction retries up to maxPostAttempts times with jittered
// exponential backoff (retryBackoff) between attempts, returning
// ErrMaxRetriesExceeded if every attempt loses the race. At this
// project's traffic levels that ceiling is not expected to be reached
// in practice; hitting it repeatedly would mean far more sustained
// concurrent writers on one account than this design targets.
func (l *Ledger) PostTransaction(ctx context.Context, p PostTransactionParams) (PostTransactionResult, error) {
	if err := validatePostTransactionParams(p); err != nil {
		return PostTransactionResult{}, err
	}

	var lastConflict error
	for attempt := 0; attempt < maxPostAttempts; attempt++ {
		result, conflict, err := l.attemptPostTransaction(ctx, p)
		if err != nil {
			return PostTransactionResult{}, err
		}
		if !conflict {
			if !result.Replayed {
				l.publishTransactionPosted(ctx, result.Transaction, result.Entries)
			}
			return result, nil
		}

		lastConflict = fmt.Errorf("attempt %d/%d: lost optimistic concurrency race", attempt+1, maxPostAttempts)
		select {
		case <-ctx.Done():
			return PostTransactionResult{}, ctx.Err()
		case <-time.After(retryBackoff(attempt)):
		}
	}

	return PostTransactionResult{}, fmt.Errorf("%w: %v", ErrMaxRetriesExceeded, lastConflict)
}

// attemptPostTransaction is one try at PostTransaction's work, entirely
// inside a single Postgres transaction. It returns conflict=true (with
// a nil error) exactly when an account update lost the optimistic
// concurrency race and the whole attempt needs to be retried -- every
// other outcome (success or a real error) is final.
func (l *Ledger) attemptPostTransaction(ctx context.Context, p PostTransactionParams) (result PostTransactionResult, conflict bool, err error) {
	tx, err := l.db.Begin(ctx)
	if err != nil {
		return PostTransactionResult{}, false, fmt.Errorf("begin transaction: %w", err)
	}
	// Safe to call after a successful Commit too -- pgx's Rollback on an
	// already-committed tx is a documented no-op, not an error we need
	// to check.
	defer tx.Rollback(ctx)

	var txn Transaction
	err = tx.QueryRow(ctx, `
		INSERT INTO transactions (idempotency_key, transaction_type, description)
		VALUES ($1, $2, $3)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id, idempotency_key, transaction_type, description, created_at
	`, p.IdempotencyKey, string(p.TransactionType), p.Description).Scan(
		&txn.ID, &txn.IdempotencyKey, &txn.TransactionType, &txn.Description, &txn.CreatedAt,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING fired: this idempotency key was
		// already processed by a prior call. Nothing to apply -- look
		// up what was posted the first time and hand it back as-is.
		return l.loadReplayedResult(ctx, tx, p.IdempotencyKey)
	}
	if err != nil {
		return PostTransactionResult{}, false, fmt.Errorf("insert transaction: %w", err)
	}

	deltas := netDeltas(p.Legs)
	for _, accountID := range sortedAccountIDs(deltas) {
		lost, err := applyBalanceDelta(ctx, tx, accountID, deltas[accountID])
		if err != nil {
			return PostTransactionResult{}, false, err
		}
		if lost {
			return PostTransactionResult{}, true, nil
		}
	}

	entries := make([]Entry, 0, len(p.Legs))
	for _, lg := range p.Legs {
		var e Entry
		err := tx.QueryRow(ctx, `
			INSERT INTO ledger_entries (transaction_id, account_id, direction, amount)
			VALUES ($1, $2, $3, $4)
			RETURNING id, transaction_id, account_id, direction, amount, created_at
		`, txn.ID, lg.AccountID, string(lg.Direction), lg.Amount).Scan(
			&e.ID, &e.TransactionID, &e.AccountID, &e.Direction, &e.Amount, &e.CreatedAt,
		)
		if err != nil {
			return PostTransactionResult{}, false, fmt.Errorf("insert ledger entry: %w", err)
		}
		entries = append(entries, e)
	}

	if err := tx.Commit(ctx); err != nil {
		return PostTransactionResult{}, false, fmt.Errorf("commit transaction: %w", err)
	}

	return PostTransactionResult{Transaction: txn, Entries: entries}, false, nil
}

// applyBalanceDelta runs the read-version-then-conditional-update step
// described in PostTransaction's doc comment for a single account,
// inside the caller's open transaction. It returns lost=true when the
// UPDATE matched zero rows because another transaction changed this
// account's version first -- the signal to abandon and retry the whole
// attempt.
func applyBalanceDelta(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, delta int64) (lost bool, err error) {
	var version int64
	err = tx.QueryRow(ctx, `SELECT version FROM accounts WHERE id = $1`, accountID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("%w: %s", ErrAccountNotFound, accountID)
	}
	if err != nil {
		return false, fmt.Errorf("read account version: %w", err)
	}

	var newVersion int64
	err = tx.QueryRow(ctx, `
		UPDATE accounts
		SET balance = balance + $1, version = version + 1, updated_at = now()
		WHERE id = $2 AND version = $3
		RETURNING version
	`, delta, accountID, version).Scan(&newVersion)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Version mismatch: someone else updated this account between
		// our SELECT and this UPDATE. Not an error -- see the doc
		// comment on PostTransaction.
		return true, nil
	case isBalanceCheckViolation(err):
		// The wallet-non-negative-balance CHECK constraint rejected
		// this update. This is a business rule, not a race: retrying
		// changes nothing, so surface it directly.
		return false, fmt.Errorf("%w: account %s", ErrInsufficientFunds, accountID)
	case err != nil:
		return false, fmt.Errorf("update account balance: %w", err)
	}

	return false, nil
}

// loadReplayedResult fetches the transaction and entries that were
// posted the first time IdempotencyKey was used, for PostTransaction's
// replay path. tx is still open at this point but read-only from here
// on -- the caller's deferred Rollback is what closes it; since this
// attempt made no writes, committing vs. rolling back has no observable
// difference, so there's no need to Commit just to look tidy.
func (l *Ledger) loadReplayedResult(ctx context.Context, tx pgx.Tx, idempotencyKey string) (PostTransactionResult, bool, error) {
	var txn Transaction
	err := tx.QueryRow(ctx, `
		SELECT id, idempotency_key, transaction_type, description, created_at
		FROM transactions
		WHERE idempotency_key = $1
	`, idempotencyKey).Scan(&txn.ID, &txn.IdempotencyKey, &txn.TransactionType, &txn.Description, &txn.CreatedAt)
	if err != nil {
		return PostTransactionResult{}, false, fmt.Errorf("look up replayed transaction: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT id, transaction_id, account_id, direction, amount, created_at
		FROM ledger_entries
		WHERE transaction_id = $1
		ORDER BY created_at ASC, id ASC
	`, txn.ID)
	if err != nil {
		return PostTransactionResult{}, false, fmt.Errorf("look up replayed entries: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.TransactionID, &e.AccountID, &e.Direction, &e.Amount, &e.CreatedAt); err != nil {
			return PostTransactionResult{}, false, fmt.Errorf("scan replayed entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return PostTransactionResult{}, false, fmt.Errorf("read replayed entry rows: %w", err)
	}

	return PostTransactionResult{Transaction: txn, Entries: entries, Replayed: true}, false, nil
}

// isBalanceCheckViolation reports whether err is Postgres rejecting an
// UPDATE because it would have violated
// accounts_wallet_balance_non_negative (migration 000001). Checking the
// constraint name, not just the generic check-violation SQLSTATE,
// avoids misclassifying some future, unrelated CHECK constraint on
// accounts as an insufficient-funds error.
func isBalanceCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == pgerrcode.CheckViolation &&
		pgErr.ConstraintName == "accounts_wallet_balance_non_negative"
}

// validatePostTransactionParams checks everything that can be checked
// without touching the database. See ErrInvalidLegs and ErrInvalidParams.
func validatePostTransactionParams(p PostTransactionParams) error {
	if p.IdempotencyKey == "" {
		return fmt.Errorf("%w: idempotency key is required", ErrInvalidParams)
	}
	if p.TransactionType != TransactionTypeTopUp && p.TransactionType != TransactionTypeTransfer {
		return fmt.Errorf("%w: unrecognized transaction type %q", ErrInvalidParams, p.TransactionType)
	}
	return validateLegs(p.Legs)
}

// validateLegs enforces the shape of a valid double-entry transaction:
// at least two legs, every amount positive, every direction recognized,
// and total debits equal to total credits. The database enforces the
// same balance rule (a deferred constraint trigger, so it only fires at
// commit) as a backstop -- this check exists so a malformed call fails
// immediately with a specific, typed error instead of an opaque trigger
// failure after a wasted round trip.
func validateLegs(legs []Leg) error {
	if len(legs) < 2 {
		return fmt.Errorf("%w: at least two legs are required, got %d", ErrInvalidLegs, len(legs))
	}

	var totalDebits, totalCredits int64
	for _, lg := range legs {
		if lg.Amount <= 0 {
			return fmt.Errorf("%w: leg amount must be positive, got %d for account %s", ErrInvalidLegs, lg.Amount, lg.AccountID)
		}
		switch lg.Direction {
		case Debit:
			totalDebits += lg.Amount
		case Credit:
			totalCredits += lg.Amount
		default:
			return fmt.Errorf("%w: unrecognized leg direction %q", ErrInvalidLegs, lg.Direction)
		}
	}

	if totalDebits != totalCredits {
		return fmt.Errorf("%w: debits (%d) do not equal credits (%d)", ErrInvalidLegs, totalDebits, totalCredits)
	}
	return nil
}

// netDeltas collapses a transaction's legs into one signed balance
// change per account. Legs are summed per account, rather than applied
// one at a time, so an account referenced by more than one leg in the
// same transaction gets exactly one UPDATE -- both because that's one
// fewer round trip, and because issuing two separate UPDATEs against
// the same row in one Postgres transaction is redundant (the second
// would just re-lock a row this transaction already holds).
func netDeltas(legs []Leg) map[uuid.UUID]int64 {
	deltas := make(map[uuid.UUID]int64, len(legs))
	for _, lg := range legs {
		deltas[lg.AccountID] += lg.Direction.delta(lg.Amount)
	}
	return deltas
}

// sortedAccountIDs returns deltas' keys sorted by raw UUID bytes. Every
// call site that updates more than one account in a transaction must
// use this same order to acquire row locks in -- see the "deadlock"
// half of PostTransaction's doc comment for why a consistent global
// order (as opposed to, say, "source account first") is what prevents
// two transactions moving money in opposite directions from deadlocking
// against each other.
func sortedAccountIDs(deltas map[uuid.UUID]int64) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(deltas))
	for id := range deltas {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	return ids
}

// retryBackoff returns how long to wait before retry number attempt+1,
// as jittered exponential backoff capped at retryMaxDelay. Backoff
// spreads out retries under contention -- if every loser of the race
// retried immediately, a hot account under heavy concurrent load would
// have every retry pile up and re-collide at once instead of spreading
// out; jitter (a random duration, not just a growing fixed one) is what
// keeps many concurrently-retrying goroutines from all waking up and
// re-attempting on the same tick.
func retryBackoff(attempt int) time.Duration {
	doublings := attempt
	if doublings > retryMaxDoubling {
		doublings = retryMaxDoubling
	}
	d := retryBaseDelay * time.Duration(1<<uint(doublings))
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	return d/2 + rand.N(d/2+1)
}
