package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/ryansurya1993/ledgerly/wallet-service/internal/ledgerclient"
)

// writeLedgerClientError maps an error from a ledgerclient.Client call
// to an HTTP response. A *ledgerclient.APIError means ledger-service
// itself answered with a considered status code (404, 409, 503, ...),
// forwarded here as-is -- ledger-service already decided what that
// failure means, and re-deriving that decision here would just
// duplicate its error-mapping logic (see CLAUDE.md's "Code reuse" rule)
// one HTTP hop later. Anything else -- a network failure, a timeout,
// ledger-service unreachable entirely -- becomes 502 Bad Gateway, since
// wallet-service is acting as a gateway in front of it.
func writeLedgerClientError(w http.ResponseWriter, err error) {
	var apiErr *ledgerclient.APIError
	if errors.As(err, &apiErr) {
		writeError(w, apiErr.StatusCode, apiErr.Message)
		return
	}
	log.Printf("handler: ledger-service call failed: %v", err)
	writeError(w, http.StatusBadGateway, "ledger-service is unavailable")
}
