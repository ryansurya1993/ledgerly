// Package handler contains wallet-service's HTTP handlers.
package handler

import (
	"context"

	"github.com/ryansurya1993/ledgerly/wallet-service/internal/idempotency"
	"github.com/ryansurya1993/ledgerly/wallet-service/internal/ledgerclient"
)

// LedgerClient is everything the wallet handlers need from
// ledger-service. *ledgerclient.Client satisfies it. Depending on this
// narrow interface, rather than *ledgerclient.Client directly, keeps
// handlers testable against a scripted fake instead of a real
// ledger-service instance -- the same reasoning ledger-service's own
// internal/handler.LedgerService applies one layer down for
// *ledger.Ledger.
type LedgerClient interface {
	CreateAccount(ctx context.Context, req ledgerclient.CreateAccountRequest) (ledgerclient.Account, error)
	PostTransaction(ctx context.Context, req ledgerclient.PostTransactionRequest) (ledgerclient.PostTransactionResponse, error)
	GetBalance(ctx context.Context, accountID string) (ledgerclient.Balance, error)
	GetHistory(ctx context.Context, accountID string) (ledgerclient.History, error)
}

// IdempotencyStore is what the topup/transfer handlers need from the
// Redis-backed fast-path cache. *idempotency.Store satisfies it.
type IdempotencyStore interface {
	Get(ctx context.Context, key string) (idempotency.CachedResponse, bool, error)
	Set(ctx context.Context, key string, resp idempotency.CachedResponse) error
}
