package handler

import "net/http"

type allIntegrityResponse struct {
	Results []integrityResponse `json:"results"`
	// Drifted is true if any account in Results drifted -- a quick
	// top-level flag for the "is everything still green" stress-test
	// widget, so a caller doesn't have to scan Results itself just to
	// answer that.
	Drifted bool `json:"drifted"`
}

// GetAllIntegrity handles GET /integrity: the ledger-wide check across
// every account.
func GetAllIntegrity(svc LedgerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		results, err := svc.CheckAllAccountsIntegrity(r.Context())
		if err != nil {
			writeLedgerError(w, err)
			return
		}

		resp := allIntegrityResponse{Results: make([]integrityResponse, len(results))}
		for i, result := range results {
			resp.Results[i] = newIntegrityResponse(result)
			if result.Drifted() {
				resp.Drifted = true
			}
		}

		writeJSON(w, http.StatusOK, resp)
	}
}
