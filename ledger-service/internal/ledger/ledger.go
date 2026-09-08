package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// queryer is the read-only subset of *pgxpool.Pool and pgx.Tx that
// helpers in this package need. Sharing it lets the same scan/lookup
// code run against either a bare pool (GetBalance, GetHistory -- no
// transaction needed for a single read) or an open transaction
// (PostTransaction's replay-lookup path), without duplicating queries.
type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// DB is everything Ledger needs from a database handle. *pgxpool.Pool
// satisfies it directly. Depending on this narrow interface, rather
// than *pgxpool.Pool itself, keeps this package's exported surface
// mockable in unit tests that don't need a real Postgres -- though the
// concurrency-sensitive paths in post_transaction.go are only
// meaningfully tested against a real database (see integration_test.go)
// since they depend on actual row-locking and constraint-check
// behavior no mock reproduces faithfully.
type DB interface {
	queryer
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Ledger is the entry point for this package's business logic. It holds
// no state of its own beyond the DB handle -- per CLAUDE.md, Postgres is
// the only source of truth, so every method here reads or writes
// through db on every call.
type Ledger struct {
	db DB
}

// New wraps db (typically a *pgxpool.Pool) in a Ledger.
func New(db DB) *Ledger {
	return &Ledger{db: db}
}

// CreateAccountParams describes a new account to create.
type CreateAccountParams struct {
	Name        string
	AccountType AccountType
	// Currency defaults to "USD" if left empty.
	Currency string
}

// CreateAccount inserts a new account with a zero starting balance.
// Balance and version both default to 0 at the database level (see
// migration 000001), so there's nothing more to initialize here.
func (l *Ledger) CreateAccount(ctx context.Context, p CreateAccountParams) (Account, error) {
	if p.Name == "" {
		return Account{}, fmt.Errorf("%w: account name is required", ErrInvalidParams)
	}
	if p.AccountType != AccountTypeWallet && p.AccountType != AccountTypeSystem {
		return Account{}, fmt.Errorf("%w: unrecognized account type %q", ErrInvalidParams, p.AccountType)
	}
	currency := p.Currency
	if currency == "" {
		currency = "USD"
	}

	var a Account
	err := l.db.QueryRow(ctx, `
		INSERT INTO accounts (name, account_type, currency)
		VALUES ($1, $2, $3)
		RETURNING id, name, account_type, currency, balance, version, created_at, updated_at
	`, p.Name, string(p.AccountType), currency).Scan(
		&a.ID, &a.Name, &a.AccountType, &a.Currency, &a.Balance, &a.Version, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return Account{}, fmt.Errorf("insert account: %w", err)
	}
	return a, nil
}

// GetBalance returns an account's current cached balance -- an O(1)
// read of accounts.balance, not a recomputation from ledger_entries
// (see CheckAccountIntegrity for that).
func (l *Ledger) GetBalance(ctx context.Context, accountID uuid.UUID) (int64, error) {
	var balance int64
	err := l.db.QueryRow(ctx, `SELECT balance FROM accounts WHERE id = $1`, accountID).Scan(&balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: %s", ErrAccountNotFound, accountID)
	}
	if err != nil {
		return 0, fmt.Errorf("read balance: %w", err)
	}
	return balance, nil
}

// AccountExists reports whether an account with the given ID exists. It's
// a minimal existence check, separate from GetBalance, for callers that
// only need a yes/no answer and shouldn't have to fetch (and discard) a
// full balance to get one -- e.g. disambiguating "account has no
// history yet" from "account doesn't exist" (GetHistory's caller in
// internal/handler), or guarding CheckAccountIntegrity below. Extracted
// here specifically so both call sites share one implementation instead
// of each re-deriving "does this account exist" from a query written
// for a different purpose (see CLAUDE.md's "Code reuse" rule).
func (l *Ledger) AccountExists(ctx context.Context, accountID uuid.UUID) (bool, error) {
	var exists bool
	err := l.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id = $1)`, accountID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check account existence: %w", err)
	}
	return exists, nil
}

// GetHistory returns every ledger entry posted against accountID, in
// chronological order (oldest first). created_at is the primary sort
// key; id is a tiebreaker for entries that land in the same instant
// under concurrent load, so ordering stays stable and deterministic
// across calls rather than depending on incidental physical row order.
func (l *Ledger) GetHistory(ctx context.Context, accountID uuid.UUID) ([]HistoryEntry, error) {
	rows, err := l.db.Query(ctx, `
		SELECT le.id, le.transaction_id, le.account_id, le.direction, le.amount, le.created_at,
		       t.transaction_type, t.description
		FROM ledger_entries le
		JOIN transactions t ON t.id = le.transaction_id
		WHERE le.account_id = $1
		ORDER BY le.created_at ASC, le.id ASC
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	var history []HistoryEntry
	for rows.Next() {
		var h HistoryEntry
		var description *string
		if err := rows.Scan(
			&h.ID, &h.TransactionID, &h.AccountID, &h.Direction, &h.Amount, &h.CreatedAt,
			&h.TransactionType, &description,
		); err != nil {
			return nil, fmt.Errorf("scan history entry: %w", err)
		}
		if description != nil {
			h.Description = *description
		}
		history = append(history, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read history rows: %w", err)
	}
	return history, nil
}
