package ledgerclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These tests run the client against an in-process httptest.Server
// standing in for ledger-service -- what's under test here is request
// construction, response decoding, and error mapping, not
// ledger-service's own behavior (which has its own tests in
// ledger-service/internal/ledger and internal/handler).

func TestCreateAccount_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/accounts" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req CreateAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Name != "Alice" || req.AccountType != "wallet" {
			t.Errorf("request = %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(Account{ID: "acc-1", Name: req.Name, AccountType: req.AccountType, Currency: "USD"})
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	account, err := c.CreateAccount(context.Background(), CreateAccountRequest{Name: "Alice", AccountType: "wallet"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if account.ID != "acc-1" {
		t.Errorf("account.ID = %q, want acc-1", account.ID)
	}
}

func TestPostTransaction_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transactions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(PostTransactionResponse{
			Transaction: Transaction{ID: "txn-1", IdempotencyKey: "key-1", TransactionType: "transfer"},
			Entries:     []Entry{{ID: "e1"}, {ID: "e2"}},
			Replayed:    false,
		})
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	resp, err := c.PostTransaction(context.Background(), PostTransactionRequest{
		IdempotencyKey:  "key-1",
		TransactionType: "transfer",
		Legs: []Leg{
			{AccountID: "a", Direction: DirectionDebit, Amount: 100},
			{AccountID: "b", Direction: DirectionCredit, Amount: 100},
		},
	})
	if err != nil {
		t.Fatalf("PostTransaction: %v", err)
	}
	if resp.Transaction.ID != "txn-1" || len(resp.Entries) != 2 || resp.Replayed {
		t.Errorf("resp = %+v", resp)
	}
}

func TestGetBalance_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/acc-1/balance" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(Balance{AccountID: "acc-1", Balance: 1500})
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	balance, err := c.GetBalance(context.Background(), "acc-1")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance.Balance != 1500 {
		t.Errorf("Balance = %d, want 1500", balance.Balance)
	}
}

func TestGetHistory_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/acc-1/history" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(History{
			AccountID: "acc-1",
			Entries:   []HistoryEntry{{ID: "e1", Amount: 500, Direction: "credit"}},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	history, err := c.GetHistory(context.Background(), "acc-1")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history.Entries) != 1 || history.Entries[0].Amount != 500 {
		t.Errorf("history = %+v", history)
	}
}

func TestGetIntegrity_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/integrity" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(IntegrityResponse{
			Results: []IntegrityResult{{AccountID: "acc-1", CachedBalance: 500, ComputedBalance: 500}},
			Drifted: false,
		})
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	integrity, err := c.GetIntegrity(context.Background())
	if err != nil {
		t.Fatalf("GetIntegrity: %v", err)
	}
	if len(integrity.Results) != 1 || integrity.Results[0].AccountID != "acc-1" {
		t.Errorf("integrity = %+v", integrity)
	}
}

func TestListAccounts_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode([]Account{
			{ID: "acc-1", Name: "Alice", AccountType: "wallet", Currency: "USD", Balance: 500},
			{ID: "acc-2", Name: "Bob", AccountType: "wallet", Currency: "USD", Balance: 0},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	accounts, err := c.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 2 || accounts[0].ID != "acc-1" || accounts[1].ID != "acc-2" {
		t.Errorf("accounts = %+v", accounts)
	}
}

func TestDo_NonSuccessStatusReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "ledger: insufficient funds: account acc-1"})
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	_, err := c.GetBalance(context.Background(), "acc-1")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusConflict {
		t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, http.StatusConflict)
	}
	if apiErr.Message != "ledger: insufficient funds: account acc-1" {
		t.Errorf("Message = %q", apiErr.Message)
	}
}

func TestDo_NonSuccessStatusWithoutJSONBodyStillReturnsAPIError(t *testing.T) {
	// Defensive: even if ledger-service (or something in front of it,
	// like a misconfigured proxy) returns a non-JSON error body, the
	// client must not blow up decoding it -- it should fall back to a
	// generic message built from the status code.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("upstream connect error"))
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	_, err := c.GetBalance(context.Background(), "acc-1")

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, http.StatusBadGateway)
	}
}

func TestDo_UnreachableServerReturnsPlainError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close immediately: nothing is listening on this URL anymore

	c := New(srv.URL, nil)
	_, err := c.GetBalance(context.Background(), "acc-1")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want a plain transport error, not *APIError", err)
	}
}
