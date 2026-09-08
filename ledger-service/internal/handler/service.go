package handler

import (
	"context"

	"github.com/google/uuid"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/ledger"
)

// LedgerService is everything the account/transaction/integrity
// handlers need from internal/ledger. *ledger.Ledger satisfies this.
// Depending on this narrow interface, rather than *ledger.Ledger
// directly, is the same reasoning as Pinger in health.go: it keeps
// these handlers testable against a scripted fake instead of a real
// database. Request parsing and error-to-status-code mapping -- what
// this package is actually responsible for -- don't need a real
// Postgres to verify; internal/ledger's own tests already cover its
// DB-dependent behavior.
type LedgerService interface {
	CreateAccount(ctx context.Context, p ledger.CreateAccountParams) (ledger.Account, error)
	AccountExists(ctx context.Context, accountID uuid.UUID) (bool, error)
	GetBalance(ctx context.Context, accountID uuid.UUID) (int64, error)
	GetHistory(ctx context.Context, accountID uuid.UUID) ([]ledger.HistoryEntry, error)
	PostTransaction(ctx context.Context, p ledger.PostTransactionParams) (ledger.PostTransactionResult, error)
	CheckAccountIntegrity(ctx context.Context, accountID uuid.UUID) (ledger.IntegrityResult, error)
	CheckAllAccountsIntegrity(ctx context.Context) ([]ledger.IntegrityResult, error)
}
