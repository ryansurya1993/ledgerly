// Package handler contains ledger-service's HTTP handlers.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Pinger is the one method Health needs from a DB connection pool. A
// narrow interface here (rather than taking *pgxpool.Pool directly)
// keeps this package from depending on pgx and makes the handler
// testable without a real database.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Health reports whether the service and its database connection are
// both up. It returns 503 rather than 200 when Postgres is unreachable,
// since a wallet-facing load balancer or orchestrator should route
// traffic away from a replica that can't actually serve requests.
func Health(db Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		status := "ok"
		code := http.StatusOK
		if err := db.Ping(ctx); err != nil {
			status = "unavailable"
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"status": status})
	}
}
