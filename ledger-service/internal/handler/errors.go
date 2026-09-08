package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/ledger"
)

// writeLedgerError maps an error from internal/ledger to an HTTP status
// code and writes a JSON error body. Errors this switch doesn't
// recognize are logged with full detail server-side and reported to the
// client as a generic 500 -- an unrecognized internal error's message
// (which may contain details like table/column names) is never echoed
// back to a caller.
func writeLedgerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ledger.ErrAccountNotFound):
		writeError(w, http.StatusNotFound, err.Error())

	case errors.Is(err, ledger.ErrInsufficientFunds):
		// The request is well-formed; the current account state just
		// conflicts with what it's asking to do -- 409, not 400.
		writeError(w, http.StatusConflict, err.Error())

	case errors.Is(err, ledger.ErrInvalidLegs), errors.Is(err, ledger.ErrInvalidParams):
		writeError(w, http.StatusBadRequest, err.Error())

	case errors.Is(err, ledger.ErrMaxRetriesExceeded):
		// Transient contention, not a permanent failure -- 503 signals
		// "retry later" rather than "this request is broken."
		writeError(w, http.StatusServiceUnavailable,
			"one or more accounts in this request are under heavy concurrent load; please retry")

	default:
		log.Printf("handler: unexpected ledger error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
