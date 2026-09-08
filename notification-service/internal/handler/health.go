// Package handler contains notification-service's HTTP handlers.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Pinger is the one method Health needs from the RabbitMQ consumer --
// mirrors ledger-service's internal/handler.Pinger for its DB pool and
// wallet-service's for its Redis client.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Health reports whether notification-service is currently connected
// to RabbitMQ. It returns 503 rather than 200 when it isn't, the same
// choice ledger-service makes for Postgres and for the same reason:
// unlike wallet-service's Redis (an optional latency optimization),
// RabbitMQ is notification-service's only reason to exist -- with it
// unreachable this replica has no events to stream, so it genuinely
// can't serve its purpose and a load balancer should know that.
//
// A 503 here does not mean the process is stuck or should be
// restarted: internal/events.Consumer.Run keeps retrying with backoff
// in the background regardless of what this endpoint reports, and
// reconnects on its own once RabbitMQ comes back -- see that package's
// doc comment. If this is ever wired into Kubernetes, that's exactly
// the distinction between a liveness probe (should this pod be
// restarted -- no) and a readiness probe (should traffic be routed
// here right now -- no, until reconnected): this endpoint answers the
// readiness question, and restarting the pod over its answer would
// not fix anything a restart-free reconnect wasn't already going to.
func Health(rabbitmq Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		status := "ok"
		code := http.StatusOK
		if err := rabbitmq.Ping(ctx); err != nil {
			status = "unavailable"
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"status": status})
	}
}
