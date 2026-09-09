package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ryansurya1993/ledgerly/wallet-service/internal/ledgerclient"
)

func TestGetIntegrity_Success(t *testing.T) {
	ledger := &fakeLedgerClient{
		getIntegrityFunc: func(ctx context.Context) (ledgerclient.IntegrityResponse, error) {
			return ledgerclient.IntegrityResponse{
				Results: []ledgerclient.IntegrityResult{
					{AccountID: "acct-1", CachedBalance: 500, ComputedBalance: 500, Drifted: false},
					{AccountID: "acct-2", CachedBalance: -500, ComputedBalance: -500, Drifted: false},
				},
				Drifted: false,
			}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/integrity", nil)
	w := httptest.NewRecorder()

	GetIntegrity(ledger)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got integrityResponse
	decodeJSON(t, w, &got)
	if len(got.Results) != 2 {
		t.Fatalf("len(Results) = %d, want 2", len(got.Results))
	}
	if got.Results[0].AccountID != "acct-1" {
		t.Errorf("Results[0].AccountID = %q, want acct-1", got.Results[0].AccountID)
	}
	if got.Drifted {
		t.Errorf("Drifted = true, want false")
	}
}

func TestGetIntegrity_LedgerServiceUnreachableMapsTo502(t *testing.T) {
	ledger := &fakeLedgerClient{
		getIntegrityFunc: func(ctx context.Context) (ledgerclient.IntegrityResponse, error) {
			return ledgerclient.IntegrityResponse{}, errPlainTransport
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/integrity", nil)
	w := httptest.NewRecorder()

	GetIntegrity(ledger)(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}
