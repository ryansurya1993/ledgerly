package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/ledger"
)

func TestPostTransaction_Success(t *testing.T) {
	fromID, toID := uuid.New(), uuid.New()
	txnID := uuid.New()
	now := time.Now().UTC()

	svc := &fakeLedgerService{
		postTransactionFunc: func(ctx context.Context, p ledger.PostTransactionParams) (ledger.PostTransactionResult, error) {
			if p.IdempotencyKey != "key-1" {
				t.Errorf("IdempotencyKey = %q, want key-1", p.IdempotencyKey)
			}
			if p.TransactionType != ledger.TransactionTypeTransfer {
				t.Errorf("TransactionType = %q, want transfer", p.TransactionType)
			}
			if len(p.Legs) != 2 {
				t.Fatalf("len(Legs) = %d, want 2", len(p.Legs))
			}
			if p.Legs[0].AccountID != fromID || p.Legs[0].Direction != ledger.Debit || p.Legs[0].Amount != 500 {
				t.Errorf("Legs[0] = %+v", p.Legs[0])
			}
			if p.Legs[1].AccountID != toID || p.Legs[1].Direction != ledger.Credit || p.Legs[1].Amount != 500 {
				t.Errorf("Legs[1] = %+v", p.Legs[1])
			}
			return ledger.PostTransactionResult{
				Transaction: ledger.Transaction{
					ID: txnID, IdempotencyKey: p.IdempotencyKey, TransactionType: p.TransactionType, CreatedAt: now,
				},
				Entries: []ledger.Entry{
					{ID: uuid.New(), TransactionID: txnID, AccountID: fromID, Direction: ledger.Debit, Amount: 500, CreatedAt: now},
					{ID: uuid.New(), TransactionID: txnID, AccountID: toID, Direction: ledger.Credit, Amount: 500, CreatedAt: now},
				},
			}, nil
		},
	}

	reqBody := `{
		"idempotency_key": "key-1",
		"transaction_type": "transfer",
		"legs": [
			{"account_id": "` + fromID.String() + `", "direction": "debit", "amount": 500},
			{"account_id": "` + toID.String() + `", "direction": "credit", "amount": 500}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/transactions", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	PostTransaction(svc)(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}

	var got postTransactionResponse
	decodeJSON(t, w, &got)
	if got.Replayed {
		t.Errorf("Replayed = true, want false")
	}
	if got.Transaction.ID != txnID {
		t.Errorf("Transaction.ID = %s, want %s", got.Transaction.ID, txnID)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2", len(got.Entries))
	}
}

func TestPostTransaction_ReplayedReturns200(t *testing.T) {
	txnID := uuid.New()
	svc := &fakeLedgerService{
		postTransactionFunc: func(ctx context.Context, p ledger.PostTransactionParams) (ledger.PostTransactionResult, error) {
			return ledger.PostTransactionResult{
				Transaction: ledger.Transaction{ID: txnID, IdempotencyKey: p.IdempotencyKey, TransactionType: p.TransactionType},
				Replayed:    true,
			}, nil
		},
	}

	reqBody := `{
		"idempotency_key": "already-used",
		"transaction_type": "transfer",
		"legs": [
			{"account_id": "` + uuid.New().String() + `", "direction": "debit", "amount": 100},
			{"account_id": "` + uuid.New().String() + `", "direction": "credit", "amount": 100}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/transactions", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	PostTransaction(svc)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}
	var got postTransactionResponse
	decodeJSON(t, w, &got)
	if !got.Replayed {
		t.Errorf("Replayed = false, want true")
	}
}

func TestPostTransaction_MalformedJSON(t *testing.T) {
	svc := &fakeLedgerService{}
	req := httptest.NewRequest(http.MethodPost, "/transactions", bytes.NewBufferString(`{not json`))
	w := httptest.NewRecorder()

	PostTransaction(svc)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestPostTransaction_MalformedLegAccountID(t *testing.T) {
	// account_id fails to unmarshal as a UUID -- caught at JSON decode
	// time, reported the same way as any other malformed body.
	svc := &fakeLedgerService{}
	reqBody := `{
		"idempotency_key": "k",
		"transaction_type": "transfer",
		"legs": [
			{"account_id": "not-a-uuid", "direction": "debit", "amount": 100},
			{"account_id": "` + uuid.New().String() + `", "direction": "credit", "amount": 100}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/transactions", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	PostTransaction(svc)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestPostTransaction_InsufficientFundsMapsTo409(t *testing.T) {
	svc := &fakeLedgerService{
		postTransactionFunc: func(ctx context.Context, p ledger.PostTransactionParams) (ledger.PostTransactionResult, error) {
			return ledger.PostTransactionResult{}, ledger.ErrInsufficientFunds
		},
	}
	reqBody := `{
		"idempotency_key": "k",
		"transaction_type": "transfer",
		"legs": [
			{"account_id": "` + uuid.New().String() + `", "direction": "debit", "amount": 100},
			{"account_id": "` + uuid.New().String() + `", "direction": "credit", "amount": 100}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/transactions", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	PostTransaction(svc)(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestPostTransaction_InvalidLegsMapsTo400(t *testing.T) {
	svc := &fakeLedgerService{
		postTransactionFunc: func(ctx context.Context, p ledger.PostTransactionParams) (ledger.PostTransactionResult, error) {
			return ledger.PostTransactionResult{}, ledger.ErrInvalidLegs
		},
	}
	reqBody := `{
		"idempotency_key": "k",
		"transaction_type": "transfer",
		"legs": [
			{"account_id": "` + uuid.New().String() + `", "direction": "debit", "amount": 100},
			{"account_id": "` + uuid.New().String() + `", "direction": "credit", "amount": 90}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/transactions", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	PostTransaction(svc)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestPostTransaction_MaxRetriesMapsTo503(t *testing.T) {
	svc := &fakeLedgerService{
		postTransactionFunc: func(ctx context.Context, p ledger.PostTransactionParams) (ledger.PostTransactionResult, error) {
			return ledger.PostTransactionResult{}, ledger.ErrMaxRetriesExceeded
		},
	}
	reqBody := `{
		"idempotency_key": "k",
		"transaction_type": "transfer",
		"legs": [
			{"account_id": "` + uuid.New().String() + `", "direction": "debit", "amount": 100},
			{"account_id": "` + uuid.New().String() + `", "direction": "credit", "amount": 100}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/transactions", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	PostTransaction(svc)(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestPostTransaction_AccountNotFoundMapsTo404(t *testing.T) {
	svc := &fakeLedgerService{
		postTransactionFunc: func(ctx context.Context, p ledger.PostTransactionParams) (ledger.PostTransactionResult, error) {
			return ledger.PostTransactionResult{}, ledger.ErrAccountNotFound
		},
	}
	reqBody := `{
		"idempotency_key": "k",
		"transaction_type": "transfer",
		"legs": [
			{"account_id": "` + uuid.New().String() + `", "direction": "debit", "amount": 100},
			{"account_id": "` + uuid.New().String() + `", "direction": "credit", "amount": 100}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/transactions", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	PostTransaction(svc)(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}
