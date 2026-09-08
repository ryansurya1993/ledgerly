package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Pinger is the one method Health needs from a Redis client -- mirrors
// ledger-service's internal/handler.Pinger for its DB pool.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Health reports whether the wallet-service process is up.
//
// Unlike ledger-service -- where an unreachable Postgres genuinely
// means "this replica cannot serve correctly", since Postgres is the
// single source of truth every request depends on -- wallet-service's
// only direct dependency besides ledger-service is Redis, and Redis is
// deliberately optional (see internal/idempotency's package doc): every
// request still completes correctly without it, just without the
// fast-path idempotency optimization. So Redis reachability is reported
// here for visibility, but never turns this into a 503 -- pulling a
// replica out of a load balancer's rotation over a degraded-but-correct
// condition would be the wrong call. Whether ledger-service itself is
// reachable is inherently a per-request concern (every handler that
// calls it already surfaces that as 502 Bad Gateway -- see errors.go),
// not something a background health check should try to predict.
func Health(redis Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		redisStatus := "ok"
		if err := redis.Ping(ctx); err != nil {
			redisStatus = "unavailable"
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
			"redis":  redisStatus,
		})
	}
}
