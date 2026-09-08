package handler

import (
	"encoding/json"
	"log"
	"net/http"
)

// writeJSON encodes v as the response body with the given status code.
// A failure to encode is logged, not surfaced to the client -- by the
// time Encode fails, WriteHeader has already been called, so there's no
// way to change the response status at that point anyway.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("handler: encode response: %v", err)
	}
}

type errorResponse struct {
	Error string `json:"error"`
}

// writeError writes a JSON error body: {"error": message}.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
