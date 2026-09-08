package ledger

import "errors"

// Sentinel errors callers (handlers, tests) can match with errors.Is.
// Wrapped with extra context via fmt.Errorf("%w: ...") at the call site,
// so the sentinel survives while still carrying a useful message.
var (
	// ErrAccountNotFound means an account ID referenced by a call (as
	// the account itself, or as a leg's account) doesn't exist.
	ErrAccountNotFound = errors.New("ledger: account not found")

	// ErrInsufficientFunds means applying a transaction's legs would
	// have driven a wallet account's balance negative. This is a
	// business-rule rejection, not a concurrency conflict -- retrying
	// the same transaction won't change the outcome, so PostTransaction
	// returns it immediately rather than looping.
	ErrInsufficientFunds = errors.New("ledger: insufficient funds")

	// ErrInvalidLegs means the legs passed to PostTransaction don't
	// form a valid double-entry transaction (fewer than two legs, a
	// non-positive amount, an unrecognized direction, or debits that
	// don't sum to credits). Checked in Go before any DB round trip so
	// callers get a precise error instead of the database's deferred
	// balance-trigger failure.
	ErrInvalidLegs = errors.New("ledger: invalid transaction legs")

	// ErrInvalidParams covers other malformed input: a missing
	// idempotency key, an unrecognized transaction or account type.
	ErrInvalidParams = errors.New("ledger: invalid parameters")

	// ErrMaxRetriesExceeded means PostTransaction's optimistic
	// concurrency retry loop (see post_transaction.go) ran out of
	// attempts without ever winning the race to update every account
	// involved. Under the load levels this project targets this should
	// be very rare; seeing it in practice is a sign of either far more
	// contention on one account than expected, or a stuck/slow
	// transaction elsewhere holding a row lock.
	ErrMaxRetriesExceeded = errors.New("ledger: exceeded max retries reconciling concurrent account updates")
)
