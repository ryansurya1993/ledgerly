package handler

import (
	"encoding/json"
	"log"
	"net/http"
)

// writeJSON encodes v as the response body with the given status code
// and returns the marshaled bytes. Returning the bytes (unlike
// ledger-service's equivalent helper, which doesn't need to) lets
// callers that also need to cache the exact response -- see
// wallets.go's idempotency fast path -- reuse them instead of
// marshaling v a second time.
func writeJSON(w http.ResponseWriter, status int, v any) []byte {
	body, err := json.Marshal(v)
	if err != nil {
		log.Printf("handler: marshal response: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
	return body
}

type errorResponse struct {
	Error string `json:"error"`
}

// writeError writes a JSON error body: {"error": message}.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
