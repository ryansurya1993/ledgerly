// Package stream fans out event payloads to every currently-connected
// SSE client. It knows nothing about RabbitMQ or the event's Go type
// -- it moves opaque []byte payloads -- so it stays reusable for
// whatever event type internal/events hands it next, without a change
// here.
package stream

import "sync"

// Hub is safe for concurrent use: Publish is called from the RabbitMQ
// consumer goroutine, Subscribe/Unsubscribe from each client
// connection's own goroutine in internal/handler.Stream.
//
// Holding open client connections in a process-local map is stateful
// in a literal sense, but not in the sense CLAUDE.md's "stateless
// services" rule cares about: that rule is about never treating
// in-memory data as a source of truth for anything durable (account
// balances, session data) that must be consistent across replicas or
// survive a restart. A live SSE connection is neither -- it can't be
// migrated to another replica or another process by definition (it's
// tied to one open TCP socket), and losing it on restart is simply
// "the client reconnects," which is the normal, expected behavior of
// any live stream, browser-native EventSource included. Nothing about
// this correctness-relevant state lives only here: RabbitMQ (not this
// map) is what guarantees every replica's Hub sees every event, and
// Postgres remains the durable source of truth for the data these
// events describe.
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[chan []byte]struct{})}
}

// subscriberBuffer is how many unconsumed payloads a slow client can
// fall behind by before Publish starts dropping events for it. Small
// on purpose: a live activity feed's value is in *recent* events, so a
// client that's fallen this far behind gains little from a deep
// backlog, and this bounds how much memory one slow client can hold up.
const subscriberBuffer = 16

// Subscribe registers a new client and returns the channel it will
// receive published payloads on. Callers must call Unsubscribe(ch)
// (typically deferred) once the client disconnects.
func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, subscriberBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes and closes ch. Safe to call exactly once per
// channel returned by Subscribe.
func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
	close(ch)
}

// Publish fans payload out to every currently-subscribed client. A
// client whose buffer is already full has its update dropped for this
// event rather than blocking Publish (and so every other client, and
// the RabbitMQ consumer goroutine that calls Publish) on one laggard --
// acceptable for a live feed where the next event will arrive shortly
// anyway; see subscriberBuffer.
func (h *Hub) Publish(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- payload:
		default:
		}
	}
}
