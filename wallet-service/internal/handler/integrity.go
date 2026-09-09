package handler

import (
	"net/http"

	"github.com/ryansurya1993/ledgerly/wallet-service/internal/ledgerclient"
)

type integrityResultResponse struct {
	AccountID       string `json:"account_id"`
	CachedBalance   int64  `json:"cached_balance"`
	ComputedBalance int64  `json:"computed_balance"`
	Drifted         bool   `json:"drifted"`
}

type integrityResponse struct {
	Results []integrityResultResponse `json:"results"`
	Drifted bool                      `json:"drifted"`
}

func newIntegrityResponse(r ledgerclient.IntegrityResponse) integrityResponse {
	results := make([]integrityResultResponse, len(r.Results))
	for i, res := range r.Results {
		results[i] = integrityResultResponse{
			AccountID:       res.AccountID,
			CachedBalance:   res.CachedBalance,
			ComputedBalance: res.ComputedBalance,
			Drifted:         res.Drifted,
		}
	}
	return integrityResponse{Results: results, Drifted: r.Drifted}
}

// GetIntegrity handles GET /integrity: a passthrough to ledger-service's
// endpoint of the same name, unlike every other handler in this
// package. It exists here (rather than requiring the frontend to call
// ledger-service directly) purely so the demo frontend never needs to
// deal with more than one origin for anything except the live SSE feed
// -- see the root README's frontend section. This is a system-wide
// health view, not a wallet-shaped resource, so results are passed
// through in ledger-service's own terms (account_id, not wallet_id --
// see ledgerclient.IntegrityResult's doc comment for why).
func GetIntegrity(ledger LedgerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		integrity, err := ledger.GetIntegrity(r.Context())
		if err != nil {
			writeLedgerClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, newIntegrityResponse(integrity))
	}
}
