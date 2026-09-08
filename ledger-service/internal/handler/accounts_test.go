package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/ledger"
)

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(w.Body).Decode(v); err != nil {
		t.Fatalf("decode response body %q: %v", w.Body.String(), err)
	}
}

func TestCreateAccount_Success(t *testing.T) {
	now := time.Now().UTC()
	wantID := uuid.New()

	svc := &fakeLedgerService{
		createAccountFunc: func(ctx context.Context, p ledger.CreateAccountParams) (ledger.Account, error) {
			if p.Name != "Alice's Wallet" {
				t.Errorf("Name = %q, want %q", p.Name, "Alice's Wallet")
			}
			if p.AccountType != ledger.AccountTypeWallet {
				t.Errorf("AccountType = %q, want %q", p.AccountType, ledger.AccountTypeWallet)
			}
			return ledger.Account{
				ID: wantID, Name: p.Name, AccountType: p.AccountType, Currency: "USD",
				Balance: 0, Version: 0, CreatedAt: now, UpdatedAt: now,
			}, nil
		},
	}

	body := bytes.NewBufferString(`{"name": "Alice's Wallet", "account_type": "wallet"}`)
	req := httptest.NewRequest(http.MethodPost, "/accounts", body)
	w := httptest.NewRecorder()

	CreateAccount(svc)(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}

	var got accountResponse
	decodeJSON(t, w, &got)
	if got.ID != wantID {
		t.Errorf("ID = %s, want %s", got.ID, wantID)
	}
	if got.AccountType != "wallet" {
		t.Errorf("AccountType = %q, want wallet", got.AccountType)
	}
}

func TestCreateAccount_MalformedJSON(t *testing.T) {
	svc := &fakeLedgerService{}
	req := httptest.NewRequest(http.MethodPost, "/accounts", bytes.NewBufferString(`{not json`))
	w := httptest.NewRecorder()

	CreateAccount(svc)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateAccount_InvalidParamsMapsTo400(t *testing.T) {
	svc := &fakeLedgerService{
		createAccountFunc: func(ctx context.Context, p ledger.CreateAccountParams) (ledger.Account, error) {
			return ledger.Account{}, ledger.ErrInvalidParams
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/accounts", bytes.NewBufferString(`{"name": "x", "account_type": "bogus"}`))
	w := httptest.NewRecorder()

	CreateAccount(svc)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestGetBalance_Success(t *testing.T) {
	accountID := uuid.New()
	svc := &fakeLedgerService{
		getBalanceFunc: func(ctx context.Context, id uuid.UUID) (int64, error) {
			if id != accountID {
				t.Errorf("id = %s, want %s", id, accountID)
			}
			return 1500, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/accounts/"+accountID.String()+"/balance", nil)
	req.SetPathValue("id", accountID.String())
	w := httptest.NewRecorder()

	GetBalance(svc)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got balanceResponse
	decodeJSON(t, w, &got)
	if got.Balance != 1500 {
		t.Errorf("Balance = %d, want 1500", got.Balance)
	}
	if got.AccountID != accountID {
		t.Errorf("AccountID = %s, want %s", got.AccountID, accountID)
	}
}

func TestGetBalance_InvalidAccountID(t *testing.T) {
	svc := &fakeLedgerService{}
	req := httptest.NewRequest(http.MethodGet, "/accounts/not-a-uuid/balance", nil)
	req.SetPathValue("id", "not-a-uuid")
	w := httptest.NewRecorder()

	GetBalance(svc)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestGetBalance_NotFoundMapsTo404(t *testing.T) {
	svc := &fakeLedgerService{
		getBalanceFunc: func(ctx context.Context, id uuid.UUID) (int64, error) {
			return 0, ledger.ErrAccountNotFound
		},
	}
	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/accounts/"+id.String()+"/balance", nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	GetBalance(svc)(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestGetHistory_Success(t *testing.T) {
	accountID := uuid.New()
	txnID := uuid.New()
	entryID := uuid.New()
	now := time.Now().UTC()

	svc := &fakeLedgerService{
		getHistoryFunc: func(ctx context.Context, id uuid.UUID) ([]ledger.HistoryEntry, error) {
			return []ledger.HistoryEntry{
				{
					Entry: ledger.Entry{
						ID: entryID, TransactionID: txnID, AccountID: id,
						Direction: ledger.Credit, Amount: 250, CreatedAt: now,
					},
					TransactionType: ledger.TransactionTypeTopUp,
					Description:     "seed",
				},
			}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/accounts/"+accountID.String()+"/history", nil)
	req.SetPathValue("id", accountID.String())
	w := httptest.NewRecorder()

	GetHistory(svc)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got historyResponse
	decodeJSON(t, w, &got)
	if len(got.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(got.Entries))
	}
	if got.Entries[0].Amount != 250 || got.Entries[0].Direction != "credit" {
		t.Errorf("entry = %+v, want amount=250 direction=credit", got.Entries[0])
	}
}

func TestGetHistory_EmptyButExistingAccountReturns200(t *testing.T) {
	var accountExistsCalled bool
	svc := &fakeLedgerService{
		getHistoryFunc: func(ctx context.Context, id uuid.UUID) ([]ledger.HistoryEntry, error) {
			return nil, nil
		},
		accountExistsFunc: func(ctx context.Context, id uuid.UUID) (bool, error) {
			accountExistsCalled = true
			return true, nil
		},
	}
	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/accounts/"+id.String()+"/history", nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	GetHistory(svc)(w, req)

	if !accountExistsCalled {
		t.Fatalf("AccountExists was not called to disambiguate the empty result")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}
	var got historyResponse
	decodeJSON(t, w, &got)
	if len(got.Entries) != 0 {
		t.Fatalf("len(Entries) = %d, want 0", len(got.Entries))
	}
}

func TestGetHistory_NonexistentAccountReturns404(t *testing.T) {
	svc := &fakeLedgerService{
		getHistoryFunc: func(ctx context.Context, id uuid.UUID) ([]ledger.HistoryEntry, error) {
			return nil, nil
		},
		accountExistsFunc: func(ctx context.Context, id uuid.UUID) (bool, error) {
			return false, nil
		},
	}
	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/accounts/"+id.String()+"/history", nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	GetHistory(svc)(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestGetHistory_WithEntriesDoesNotCallAccountExists(t *testing.T) {
	// The common-case path (an account that has history) must resolve
	// in the one GetHistory query -- AccountExists is only for
	// disambiguating an empty result, so it must not fire here.
	svc := &fakeLedgerService{
		getHistoryFunc: func(ctx context.Context, id uuid.UUID) ([]ledger.HistoryEntry, error) {
			return []ledger.HistoryEntry{
				{Entry: ledger.Entry{ID: uuid.New(), AccountID: id, Direction: ledger.Credit, Amount: 10}},
			}, nil
		},
		accountExistsFunc: func(ctx context.Context, id uuid.UUID) (bool, error) {
			t.Fatalf("AccountExists should not be called when GetHistory already returned entries")
			return false, nil
		},
	}
	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/accounts/"+id.String()+"/history", nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	GetHistory(svc)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestGetAccountIntegrity_NoDrift(t *testing.T) {
	id := uuid.New()
	svc := &fakeLedgerService{
		checkAccountIntegrityFunc: func(ctx context.Context, accountID uuid.UUID) (ledger.IntegrityResult, error) {
			return ledger.IntegrityResult{AccountID: accountID, CachedBalance: 100, ComputedBalance: 100}, nil
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/accounts/"+id.String()+"/integrity", nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	GetAccountIntegrity(svc)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got integrityResponse
	decodeJSON(t, w, &got)
	if got.Drifted {
		t.Errorf("Drifted = true, want false")
	}
}

func TestGetAccountIntegrity_Drifted(t *testing.T) {
	id := uuid.New()
	svc := &fakeLedgerService{
		checkAccountIntegrityFunc: func(ctx context.Context, accountID uuid.UUID) (ledger.IntegrityResult, error) {
			return ledger.IntegrityResult{AccountID: accountID, CachedBalance: 100, ComputedBalance: 90}, nil
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/accounts/"+id.String()+"/integrity", nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	GetAccountIntegrity(svc)(w, req)

	var got integrityResponse
	decodeJSON(t, w, &got)
	if !got.Drifted {
		t.Errorf("Drifted = false, want true (cached=100 computed=90)")
	}
	if got.CachedBalance != 100 || got.ComputedBalance != 90 {
		t.Errorf("got = %+v", got)
	}
}

func TestGetAccountIntegrity_NonexistentAccountReturns404(t *testing.T) {
	// internal/ledger.CheckAccountIntegrity checks AccountExists itself
	// before computing anything, so the handler doesn't need its own
	// existence check here -- it just needs to map ErrAccountNotFound
	// correctly, same as every other handler.
	svc := &fakeLedgerService{
		checkAccountIntegrityFunc: func(ctx context.Context, accountID uuid.UUID) (ledger.IntegrityResult, error) {
			return ledger.IntegrityResult{}, ledger.ErrAccountNotFound
		},
	}
	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/accounts/"+id.String()+"/integrity", nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	GetAccountIntegrity(svc)(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusNotFound, w.Body.String())
	}
}
