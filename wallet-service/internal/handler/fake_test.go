package handler

import (
	"context"

	"github.com/ryansurya1993/ledgerly/wallet-service/internal/idempotency"
	"github.com/ryansurya1993/ledgerly/wallet-service/internal/ledgerclient"
)

// fakeLedgerClient is a scriptable LedgerClient for handler tests --
// mirrors ledger-service's own internal/handler.fakeLedgerService.
// What's under test in this package is request parsing/validation, leg
// construction, and error mapping, not ledger-service's own behavior.
type fakeLedgerClient struct {
	createAccountFunc   func(ctx context.Context, req ledgerclient.CreateAccountRequest) (ledgerclient.Account, error)
	postTransactionFunc func(ctx context.Context, req ledgerclient.PostTransactionRequest) (ledgerclient.PostTransactionResponse, error)
	getBalanceFunc      func(ctx context.Context, accountID string) (ledgerclient.Balance, error)
	getHistoryFunc      func(ctx context.Context, accountID string) (ledgerclient.History, error)
}

func (f *fakeLedgerClient) CreateAccount(ctx context.Context, req ledgerclient.CreateAccountRequest) (ledgerclient.Account, error) {
	return f.createAccountFunc(ctx, req)
}

func (f *fakeLedgerClient) PostTransaction(ctx context.Context, req ledgerclient.PostTransactionRequest) (ledgerclient.PostTransactionResponse, error) {
	return f.postTransactionFunc(ctx, req)
}

func (f *fakeLedgerClient) GetBalance(ctx context.Context, accountID string) (ledgerclient.Balance, error) {
	return f.getBalanceFunc(ctx, accountID)
}

func (f *fakeLedgerClient) GetHistory(ctx context.Context, accountID string) (ledgerclient.History, error) {
	return f.getHistoryFunc(ctx, accountID)
}

// fakeIdempotencyStore is an in-memory, scriptable IdempotencyStore for
// handler tests -- a real Redis isn't needed to test the handler's
// caching *logic* (when it checks, when it writes, when it doesn't),
// only to test the Store implementation itself (see
// internal/idempotency's own tests).
type fakeIdempotencyStore struct {
	entries map[string]idempotency.CachedResponse

	// getErr/setErr, when non-nil, make the next Get/Set call fail --
	// used to test that a Redis error degrades to "cache miss" /
	// "logged and ignored" rather than failing the request.
	getErr error
	setErr error

	// setCalls records every Set call, so tests can assert whether
	// caching happened (or, for error responses, that it didn't).
	setCalls []idempotency.CachedResponse
}

func newFakeIdempotencyStore() *fakeIdempotencyStore {
	return &fakeIdempotencyStore{entries: map[string]idempotency.CachedResponse{}}
}

func (f *fakeIdempotencyStore) Get(ctx context.Context, key string) (idempotency.CachedResponse, bool, error) {
	if f.getErr != nil {
		return idempotency.CachedResponse{}, false, f.getErr
	}
	resp, found := f.entries[key]
	return resp, found, nil
}

func (f *fakeIdempotencyStore) Set(ctx context.Context, key string, resp idempotency.CachedResponse) error {
	f.setCalls = append(f.setCalls, resp)
	if f.setErr != nil {
		return f.setErr
	}
	f.entries[key] = resp
	return nil
}
