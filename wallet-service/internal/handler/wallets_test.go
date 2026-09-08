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

	"github.com/ryansurya1993/ledgerly/wallet-service/internal/idempotency"
	"github.com/ryansurya1993/ledgerly/wallet-service/internal/ledgerclient"
)

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(w.Body).Decode(v); err != nil {
		t.Fatalf("decode response body %q: %v", w.Body.String(), err)
	}
}

func TestCreateWallet_Success(t *testing.T) {
	now := time.Now().UTC()
	ledger := &fakeLedgerClient{
		createAccountFunc: func(ctx context.Context, req ledgerclient.CreateAccountRequest) (ledgerclient.Account, error) {
			if req.AccountType != "wallet" {
				t.Errorf("AccountType = %q, want wallet", req.AccountType)
			}
			if req.Name != "Alice" {
				t.Errorf("Name = %q, want Alice", req.Name)
			}
			return ledgerclient.Account{
				ID: "acc-1", Name: req.Name, AccountType: req.AccountType, Currency: "USD",
				Balance: 0, Version: 0, CreatedAt: now, UpdatedAt: now,
			}, nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/wallets", bytes.NewBufferString(`{"name": "Alice"}`))
	w := httptest.NewRecorder()

	CreateWallet(ledger)(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}

	// account_type and version are ledger implementation details -- the
	// wallet-shaped response must not leak them. Checked against the raw
	// JSON (not the typed walletResponse, which by construction can't
	// have fields it doesn't declare) so this actually catches a
	// regression if those fields were ever added back to the struct.
	var raw map[string]any
	decodeJSON(t, w, &raw)
	if _, present := raw["account_type"]; present {
		t.Errorf("response leaks account_type: %v", raw)
	}
	if _, present := raw["version"]; present {
		t.Errorf("response leaks version: %v", raw)
	}
	if raw["id"] != "acc-1" {
		t.Errorf("id = %v, want acc-1", raw["id"])
	}
}

func TestCreateWallet_MissingNameIs400(t *testing.T) {
	ledger := &fakeLedgerClient{}
	req := httptest.NewRequest(http.MethodPost, "/wallets", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()

	CreateWallet(ledger)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateWallet_MalformedJSON(t *testing.T) {
	ledger := &fakeLedgerClient{}
	req := httptest.NewRequest(http.MethodPost, "/wallets", bytes.NewBufferString(`{not json`))
	w := httptest.NewRecorder()

	CreateWallet(ledger)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateWallet_LedgerErrorMapsThrough(t *testing.T) {
	ledger := &fakeLedgerClient{
		createAccountFunc: func(ctx context.Context, req ledgerclient.CreateAccountRequest) (ledgerclient.Account, error) {
			return ledgerclient.Account{}, &ledgerclient.APIError{StatusCode: http.StatusBadRequest, Message: "boom"}
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/wallets", bytes.NewBufferString(`{"name": "Alice"}`))
	w := httptest.NewRecorder()

	CreateWallet(ledger)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestGetWalletBalance_Success(t *testing.T) {
	id := uuid.New()
	ledger := &fakeLedgerClient{
		getBalanceFunc: func(ctx context.Context, accountID string) (ledgerclient.Balance, error) {
			if accountID != id.String() {
				t.Errorf("accountID = %q, want %q", accountID, id)
			}
			return ledgerclient.Balance{AccountID: accountID, Balance: 750}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/wallets/"+id.String()+"/balance", nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	GetWalletBalance(ledger)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got balanceResponse
	decodeJSON(t, w, &got)
	if got.Balance != 750 {
		t.Errorf("Balance = %d, want 750", got.Balance)
	}
}

func TestGetWalletBalance_InvalidID(t *testing.T) {
	ledger := &fakeLedgerClient{}
	req := httptest.NewRequest(http.MethodGet, "/wallets/not-a-uuid/balance", nil)
	req.SetPathValue("id", "not-a-uuid")
	w := httptest.NewRecorder()

	GetWalletBalance(ledger)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestGetWalletBalance_NotFoundMapsThrough(t *testing.T) {
	id := uuid.New()
	ledger := &fakeLedgerClient{
		getBalanceFunc: func(ctx context.Context, accountID string) (ledgerclient.Balance, error) {
			return ledgerclient.Balance{}, &ledgerclient.APIError{StatusCode: http.StatusNotFound, Message: "not found"}
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/wallets/"+id.String()+"/balance", nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	GetWalletBalance(ledger)(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestGetWalletBalance_LedgerServiceUnreachableMapsTo502(t *testing.T) {
	id := uuid.New()
	ledger := &fakeLedgerClient{
		getBalanceFunc: func(ctx context.Context, accountID string) (ledgerclient.Balance, error) {
			return ledgerclient.Balance{}, errPlainTransport
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/wallets/"+id.String()+"/balance", nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	GetWalletBalance(ledger)(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

func TestGetWalletHistory_Success(t *testing.T) {
	id := uuid.New()
	ledger := &fakeLedgerClient{
		getHistoryFunc: func(ctx context.Context, accountID string) (ledgerclient.History, error) {
			return ledgerclient.History{
				AccountID: accountID,
				Entries: []ledgerclient.HistoryEntry{
					{ID: "e1", TransactionType: "top_up", Direction: "credit", Amount: 500},
				},
			}, nil
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/wallets/"+id.String()+"/history", nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	GetWalletHistory(ledger)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got historyResponse
	decodeJSON(t, w, &got)
	if len(got.Entries) != 1 || got.Entries[0].Amount != 500 {
		t.Errorf("Entries = %+v", got.Entries)
	}
}

var errPlainTransport = errPlain("connection refused")

type errPlain string

func (e errPlain) Error() string { return string(e) }

func TestTopUp_Success(t *testing.T) {
	walletID := uuid.New()
	now := time.Now().UTC()
	ledger := &fakeLedgerClient{
		postTransactionFunc: func(ctx context.Context, req ledgerclient.PostTransactionRequest) (ledgerclient.PostTransactionResponse, error) {
			if req.TransactionType != ledgerclient.TransactionTypeTopUp {
				t.Errorf("TransactionType = %q, want top_up", req.TransactionType)
			}
			if len(req.Legs) != 2 {
				t.Fatalf("len(Legs) = %d, want 2", len(req.Legs))
			}
			if req.Legs[0].AccountID != ledgerclient.ExternalFundingAccountID || req.Legs[0].Direction != ledgerclient.DirectionDebit {
				t.Errorf("Legs[0] = %+v", req.Legs[0])
			}
			if req.Legs[1].AccountID != walletID.String() || req.Legs[1].Direction != ledgerclient.DirectionCredit {
				t.Errorf("Legs[1] = %+v", req.Legs[1])
			}
			return ledgerclient.PostTransactionResponse{
				Transaction: ledgerclient.Transaction{ID: "txn-1", IdempotencyKey: req.IdempotencyKey, CreatedAt: now},
				Replayed:    false,
			}, nil
		},
	}
	idem := newFakeIdempotencyStore()

	body := `{"idempotency_key": "key-1", "amount": 500}`
	req := httptest.NewRequest(http.MethodPost, "/wallets/"+walletID.String()+"/topup", bytes.NewBufferString(body))
	req.SetPathValue("id", walletID.String())
	w := httptest.NewRecorder()

	TopUp(ledger, idem)(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}
	var got topUpResponse
	decodeJSON(t, w, &got)
	if got.TransactionID != "txn-1" || got.WalletID != walletID.String() || got.Amount != 500 {
		t.Errorf("got = %+v", got)
	}

	if len(idem.setCalls) != 1 {
		t.Fatalf("Set was called %d times, want 1 (successful response should be cached)", len(idem.setCalls))
	}
	if idem.setCalls[0].StatusCode != http.StatusCreated {
		t.Errorf("cached StatusCode = %d, want %d", idem.setCalls[0].StatusCode, http.StatusCreated)
	}
}

func TestTopUp_MissingIdempotencyKeyIs400(t *testing.T) {
	ledger := &fakeLedgerClient{}
	idem := newFakeIdempotencyStore()
	req := httptest.NewRequest(http.MethodPost, "/wallets/"+uuid.NewString()+"/topup", bytes.NewBufferString(`{"amount": 500}`))
	req.SetPathValue("id", uuid.NewString())
	w := httptest.NewRecorder()

	TopUp(ledger, idem)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestTopUp_NonPositiveAmountIs400(t *testing.T) {
	ledger := &fakeLedgerClient{}
	idem := newFakeIdempotencyStore()
	req := httptest.NewRequest(http.MethodPost, "/wallets/"+uuid.NewString()+"/topup",
		bytes.NewBufferString(`{"idempotency_key": "k", "amount": 0}`))
	req.SetPathValue("id", uuid.NewString())
	w := httptest.NewRecorder()

	TopUp(ledger, idem)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestTopUp_IdempotencyCacheHitSkipsLedgerService(t *testing.T) {
	walletID := uuid.New()
	ledger := &fakeLedgerClient{
		postTransactionFunc: func(ctx context.Context, req ledgerclient.PostTransactionRequest) (ledgerclient.PostTransactionResponse, error) {
			t.Fatal("ledger-service should not be called on an idempotency cache hit")
			return ledgerclient.PostTransactionResponse{}, nil
		},
	}
	idem := newFakeIdempotencyStore()
	cachedBody := []byte(`{"transaction_id":"txn-cached","wallet_id":"w","amount":500,"replayed":false}`)
	idem.entries["key-1"] = idempotency.CachedResponse{StatusCode: http.StatusCreated, Body: cachedBody}

	req := httptest.NewRequest(http.MethodPost, "/wallets/"+walletID.String()+"/topup",
		bytes.NewBufferString(`{"idempotency_key": "key-1", "amount": 500}`))
	req.SetPathValue("id", walletID.String())
	w := httptest.NewRecorder()

	TopUp(ledger, idem)(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusCreated)
	}
	if w.Body.String() != string(cachedBody) {
		t.Errorf("body = %s, want the cached body verbatim: %s", w.Body.String(), cachedBody)
	}
}

func TestTopUp_IdempotencyCacheErrorFallsThroughToLedgerService(t *testing.T) {
	walletID := uuid.New()
	var called bool
	ledger := &fakeLedgerClient{
		postTransactionFunc: func(ctx context.Context, req ledgerclient.PostTransactionRequest) (ledgerclient.PostTransactionResponse, error) {
			called = true
			return ledgerclient.PostTransactionResponse{Transaction: ledgerclient.Transaction{ID: "txn-1"}}, nil
		},
	}
	idem := newFakeIdempotencyStore()
	idem.getErr = errPlain("redis: connection refused")

	req := httptest.NewRequest(http.MethodPost, "/wallets/"+walletID.String()+"/topup",
		bytes.NewBufferString(`{"idempotency_key": "key-1", "amount": 500}`))
	req.SetPathValue("id", walletID.String())
	w := httptest.NewRecorder()

	TopUp(ledger, idem)(w, req)

	if !called {
		t.Fatal("ledger-service was not called after a Redis Get error -- should degrade gracefully, not fail the request")
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}
}

func TestTopUp_FailureIsNotCached(t *testing.T) {
	walletID := uuid.New()
	ledger := &fakeLedgerClient{
		postTransactionFunc: func(ctx context.Context, req ledgerclient.PostTransactionRequest) (ledgerclient.PostTransactionResponse, error) {
			return ledgerclient.PostTransactionResponse{}, &ledgerclient.APIError{StatusCode: http.StatusServiceUnavailable, Message: "contention"}
		},
	}
	idem := newFakeIdempotencyStore()

	req := httptest.NewRequest(http.MethodPost, "/wallets/"+walletID.String()+"/topup",
		bytes.NewBufferString(`{"idempotency_key": "key-1", "amount": 500}`))
	req.SetPathValue("id", walletID.String())
	w := httptest.NewRecorder()

	TopUp(ledger, idem)(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if len(idem.setCalls) != 0 {
		t.Fatalf("Set was called %d times, want 0 -- a failed/transient response must not be cached", len(idem.setCalls))
	}
}

func TestTopUp_CacheSetErrorDoesNotFailRequest(t *testing.T) {
	walletID := uuid.New()
	ledger := &fakeLedgerClient{
		postTransactionFunc: func(ctx context.Context, req ledgerclient.PostTransactionRequest) (ledgerclient.PostTransactionResponse, error) {
			return ledgerclient.PostTransactionResponse{Transaction: ledgerclient.Transaction{ID: "txn-1"}}, nil
		},
	}
	idem := newFakeIdempotencyStore()
	idem.setErr = errPlain("redis: connection refused")

	req := httptest.NewRequest(http.MethodPost, "/wallets/"+walletID.String()+"/topup",
		bytes.NewBufferString(`{"idempotency_key": "key-1", "amount": 500}`))
	req.SetPathValue("id", walletID.String())
	w := httptest.NewRecorder()

	TopUp(ledger, idem)(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d -- a cache Set failure must not fail an otherwise-successful request", w.Code, http.StatusCreated)
	}
}

func TestTopUp_ReplayedFromLedgerServiceReturns200(t *testing.T) {
	walletID := uuid.New()
	ledger := &fakeLedgerClient{
		postTransactionFunc: func(ctx context.Context, req ledgerclient.PostTransactionRequest) (ledgerclient.PostTransactionResponse, error) {
			return ledgerclient.PostTransactionResponse{
				Transaction: ledgerclient.Transaction{ID: "txn-1", IdempotencyKey: req.IdempotencyKey},
				Replayed:    true,
			}, nil
		},
	}
	idem := newFakeIdempotencyStore()

	req := httptest.NewRequest(http.MethodPost, "/wallets/"+walletID.String()+"/topup",
		bytes.NewBufferString(`{"idempotency_key": "key-1", "amount": 500}`))
	req.SetPathValue("id", walletID.String())
	w := httptest.NewRecorder()

	TopUp(ledger, idem)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d for a ledger-service-reported replay", w.Code, http.StatusOK)
	}
	var got topUpResponse
	decodeJSON(t, w, &got)
	if !got.Replayed {
		t.Errorf("Replayed = false, want true")
	}
}

func TestTransfer_Success(t *testing.T) {
	sourceID, destID := uuid.New(), uuid.New()
	ledger := &fakeLedgerClient{
		postTransactionFunc: func(ctx context.Context, req ledgerclient.PostTransactionRequest) (ledgerclient.PostTransactionResponse, error) {
			if req.TransactionType != ledgerclient.TransactionTypeTransfer {
				t.Errorf("TransactionType = %q, want transfer", req.TransactionType)
			}
			if req.Legs[0].AccountID != sourceID.String() || req.Legs[0].Direction != ledgerclient.DirectionDebit {
				t.Errorf("Legs[0] = %+v", req.Legs[0])
			}
			if req.Legs[1].AccountID != destID.String() || req.Legs[1].Direction != ledgerclient.DirectionCredit {
				t.Errorf("Legs[1] = %+v", req.Legs[1])
			}
			return ledgerclient.PostTransactionResponse{Transaction: ledgerclient.Transaction{ID: "txn-1"}}, nil
		},
	}
	idem := newFakeIdempotencyStore()

	body := `{"idempotency_key": "key-1", "destination_wallet_id": "` + destID.String() + `", "amount": 200}`
	req := httptest.NewRequest(http.MethodPost, "/wallets/"+sourceID.String()+"/transfer", bytes.NewBufferString(body))
	req.SetPathValue("id", sourceID.String())
	w := httptest.NewRecorder()

	Transfer(ledger, idem)(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}
	var got transferResponse
	decodeJSON(t, w, &got)
	if got.SourceWalletID != sourceID.String() || got.DestinationWalletID != destID.String() {
		t.Errorf("got = %+v", got)
	}
}

func TestTransfer_InvalidDestinationID(t *testing.T) {
	ledger := &fakeLedgerClient{}
	idem := newFakeIdempotencyStore()
	sourceID := uuid.New()

	body := `{"idempotency_key": "key-1", "destination_wallet_id": "not-a-uuid", "amount": 200}`
	req := httptest.NewRequest(http.MethodPost, "/wallets/"+sourceID.String()+"/transfer", bytes.NewBufferString(body))
	req.SetPathValue("id", sourceID.String())
	w := httptest.NewRecorder()

	Transfer(ledger, idem)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestTransfer_SelfTransferRejected(t *testing.T) {
	ledger := &fakeLedgerClient{
		postTransactionFunc: func(ctx context.Context, req ledgerclient.PostTransactionRequest) (ledgerclient.PostTransactionResponse, error) {
			t.Fatal("ledger-service should not be called for a rejected self-transfer")
			return ledgerclient.PostTransactionResponse{}, nil
		},
	}
	idem := newFakeIdempotencyStore()
	walletID := uuid.New()

	body := `{"idempotency_key": "key-1", "destination_wallet_id": "` + walletID.String() + `", "amount": 200}`
	req := httptest.NewRequest(http.MethodPost, "/wallets/"+walletID.String()+"/transfer", bytes.NewBufferString(body))
	req.SetPathValue("id", walletID.String())
	w := httptest.NewRecorder()

	Transfer(ledger, idem)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestTransfer_InsufficientFundsMapsThrough(t *testing.T) {
	ledger := &fakeLedgerClient{
		postTransactionFunc: func(ctx context.Context, req ledgerclient.PostTransactionRequest) (ledgerclient.PostTransactionResponse, error) {
			return ledgerclient.PostTransactionResponse{}, &ledgerclient.APIError{StatusCode: http.StatusConflict, Message: "insufficient funds"}
		},
	}
	idem := newFakeIdempotencyStore()
	sourceID, destID := uuid.New(), uuid.New()

	body := `{"idempotency_key": "key-1", "destination_wallet_id": "` + destID.String() + `", "amount": 200}`
	req := httptest.NewRequest(http.MethodPost, "/wallets/"+sourceID.String()+"/transfer", bytes.NewBufferString(body))
	req.SetPathValue("id", sourceID.String())
	w := httptest.NewRecorder()

	Transfer(ledger, idem)(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
	}
}
