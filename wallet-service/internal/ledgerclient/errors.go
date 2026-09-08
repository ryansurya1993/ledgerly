package ledgerclient

import "fmt"

// APIError is a non-2xx JSON error response from ledger-service, in the
// {"error": "..."} shape its handlers return (see
// ledger-service/internal/handler/errors.go). StatusCode is
// ledger-service's own already-considered HTTP status for the failure
// -- wallet-service's handlers generally forward it as-is rather than
// re-deriving a mapping of their own, since ledger-service already
// decided what each failure means (404 for a missing account, 409 for
// insufficient funds, 503 for concurrency contention, and so on).
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ledger-service: %d: %s", e.StatusCode, e.Message)
}
