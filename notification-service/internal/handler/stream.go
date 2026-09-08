package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/ryansurya1993/ledgerly/notification-service/internal/stream"
)

// keepaliveInterval bounds how long the connection can go silent
// between real events. Without this, an idle proxy or load balancer
// sitting between a browser and this service could time out and drop
// an otherwise-healthy connection during a quiet period with no new
// transactions -- a blank SSE comment line resets that idle timer
// without the browser's EventSource ever seeing it as a message (see
// the "event: ping" comment below).
const keepaliveInterval = 15 * time.Second

// Stream serves GET /events: a Server-Sent Events (SSE) endpoint that
// streams every subsequent TransactionPostedEvent live, for the future
// frontend's activity feed to render as it happens.
//
// # Why SSE, not a WebSocket
//
// This is a one-way, server-to-client-only feed -- the client never
// sends anything back over this connection, it only listens. SSE is
// built for exactly that shape and costs nothing extra to get: it's
// plain HTTP (this handler needs only net/http and http.Flusher, no
// extra dependency, unlike a WebSocket implementation in Go, which
// needs a third-party library since net/http has no WebSocket support
// built in), and browsers' native EventSource API reconnects
// automatically on a dropped connection with no client code required.
// A WebSocket would be the better choice the moment this needs to
// carry client-to-server messages too (e.g. the frontend sending
// filters or acknowledgements) -- it doesn't today, so the simpler
// tool wins per CLAUDE.md's "smallest tool that fits current scope."
//
// # Why fields are pre-encoded JSON, not re-encoded per client
//
// The payload handed to internal/stream.Hub is already
// json.Marshal'd once, by the caller in cmd/notification-service, and
// broadcast to every subscriber as the same []byte -- not
// re-marshaled per connected client. With potentially many concurrent
// SSE clients, that's one encode per event instead of one per
// (event, client) pair.
func Stream(hub *stream.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// This is a public, read-only, unauthenticated activity feed --
		// no cookies, no per-user data -- so a permissive CORS policy
		// lets the frontend (likely served from a different origin in
		// dev, and possibly in production) connect directly. Revisit if
		// this feed ever carries anything account-specific.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ch := hub.Subscribe()
		defer hub.Unsubscribe(ch)

		keepalive := time.NewTicker(keepaliveInterval)
		defer keepalive.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case payload, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprintf(w, "event: transaction.posted\ndata: %s\n\n", payload)
				flusher.Flush()
			case <-keepalive.C:
				fmt.Fprint(w, ": keepalive\n\n")
				flusher.Flush()
			}
		}
	}
}
