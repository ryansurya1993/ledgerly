package handler

import (
	"context"

	"github.com/google/uuid"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/ledger"
)

// fakeLedgerService is a scriptable LedgerService for handler tests.
// What's under test in this package is request parsing/validation and
// error-to-status-code mapping -- not internal/ledger's own logic,
// which has its own DB-backed tests in internal/ledger. A test sets
// only the *Func field(s) the handler under test will actually call;
// any other field being invoked unexpectedly panics on the nil call,
// which is deliberate -- it'd mean the test's assumption about what the
// handler does was wrong.
type fakeLedgerService struct {
	createAccountFunc         func(ctx context.Context, p ledger.CreateAccountParams) (ledger.Account, error)
	accountExistsFunc         func(ctx context.Context, accountID uuid.UUID) (bool, error)
	getBalanceFunc            func(ctx context.Context, accountID uuid.UUID) (int64, error)
	getHistoryFunc            func(ctx context.Context, accountID uuid.UUID) ([]ledger.HistoryEntry, error)
	listWalletAccountsFunc    func(ctx context.Context) ([]ledger.Account, error)
	postTransactionFunc       func(ctx context.Context, p ledger.PostTransactionParams) (ledger.PostTransactionResult, error)
	checkAccountIntegrityFunc func(ctx context.Context, accountID uuid.UUID) (ledger.IntegrityResult, error)
	checkAllIntegrityFunc     func(ctx context.Context) ([]ledger.IntegrityResult, error)
}

func (f *fakeLedgerService) CreateAccount(ctx context.Context, p ledger.CreateAccountParams) (ledger.Account, error) {
	return f.createAccountFunc(ctx, p)
}

func (f *fakeLedgerService) AccountExists(ctx context.Context, accountID uuid.UUID) (bool, error) {
	return f.accountExistsFunc(ctx, accountID)
}

func (f *fakeLedgerService) GetBalance(ctx context.Context, accountID uuid.UUID) (int64, error) {
	return f.getBalanceFunc(ctx, accountID)
}

func (f *fakeLedgerService) GetHistory(ctx context.Context, accountID uuid.UUID) ([]ledger.HistoryEntry, error) {
	return f.getHistoryFunc(ctx, accountID)
}

func (f *fakeLedgerService) ListWalletAccounts(ctx context.Context) ([]ledger.Account, error) {
	return f.listWalletAccountsFunc(ctx)
}

func (f *fakeLedgerService) PostTransaction(ctx context.Context, p ledger.PostTransactionParams) (ledger.PostTransactionResult, error) {
	return f.postTransactionFunc(ctx, p)
}

func (f *fakeLedgerService) CheckAccountIntegrity(ctx context.Context, accountID uuid.UUID) (ledger.IntegrityResult, error) {
	return f.checkAccountIntegrityFunc(ctx, accountID)
}

func (f *fakeLedgerService) CheckAllAccountsIntegrity(ctx context.Context) ([]ledger.IntegrityResult, error) {
	return f.checkAllIntegrityFunc(ctx)
}
