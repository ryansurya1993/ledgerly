package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/ledger"
)

func TestGetAllIntegrity_NoDrift(t *testing.T) {
	svc := &fakeLedgerService{
		checkAllIntegrityFunc: func(ctx context.Context) ([]ledger.IntegrityResult, error) {
			return []ledger.IntegrityResult{
				{AccountID: uuid.New(), CachedBalance: 100, ComputedBalance: 100},
				{AccountID: uuid.New(), CachedBalance: -50, ComputedBalance: -50},
			}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/integrity", nil)
	w := httptest.NewRecorder()

	GetAllIntegrity(svc)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got allIntegrityResponse
	decodeJSON(t, w, &got)
	if got.Drifted {
		t.Errorf("Drifted = true, want false")
	}
	if len(got.Results) != 2 {
		t.Fatalf("len(Results) = %d, want 2", len(got.Results))
	}
}

func TestGetAllIntegrity_AnyDriftedSetsTopLevelFlag(t *testing.T) {
	svc := &fakeLedgerService{
		checkAllIntegrityFunc: func(ctx context.Context) ([]ledger.IntegrityResult, error) {
			return []ledger.IntegrityResult{
				{AccountID: uuid.New(), CachedBalance: 100, ComputedBalance: 100},
				{AccountID: uuid.New(), CachedBalance: 100, ComputedBalance: 80}, // drifted
			}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/integrity", nil)
	w := httptest.NewRecorder()

	GetAllIntegrity(svc)(w, req)

	var got allIntegrityResponse
	decodeJSON(t, w, &got)
	if !got.Drifted {
		t.Errorf("Drifted = false, want true (one account disagrees)")
	}
}

func TestGetAllIntegrity_ErrorMapsTo500(t *testing.T) {
	svc := &fakeLedgerService{
		checkAllIntegrityFunc: func(ctx context.Context) ([]ledger.IntegrityResult, error) {
			return nil, context.DeadlineExceeded
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/integrity", nil)
	w := httptest.NewRecorder()

	GetAllIntegrity(svc)(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}
