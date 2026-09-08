// Package events consumes ledger-service's transaction-posted events
// from RabbitMQ and hands each one to a callback -- cmd/notification-service
// wires that callback to internal/stream.Hub, which fans events out to
// connected SSE clients.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// dialTimeout bounds a single connect attempt, so a broker that's
// merely slow to accept TCP connections (as opposed to fully down)
// still gets picked up by Run's retry loop in bounded time.
const dialTimeout = 5 * time.Second

// Consumer connects to RabbitMQ, consumes TransactionPostedEvent
// messages, and calls onEvent for each one it can decode.
//
// # Fan-out, not a shared work queue
//
// Every Consumer -- meaning every notification-service replica, and
// every reconnect of a single replica -- declares its own anonymous,
// exclusive, auto-delete queue and binds it to ExchangeName itself,
// rather than all replicas sharing one named queue. This is
// deliberate, and it's the one genuinely easy-to-get-wrong decision in
// this package: RabbitMQ's normal behavior for multiple consumers on
// the *same* named queue is competing-consumer load balancing --
// each message goes to exactly one consumer, round-robin. That's
// correct for a work queue (e.g. "process this job exactly once"), but
// it would be a real bug here: notification-service drives a live
// activity feed meant to broadcast every event to every connected
// browser client, on every replica, not distribute events across
// replicas so each client only sees a fraction of them. A private
// queue per consumer is what makes this a true fan-out (RabbitMQ's
// standard "pub/sub" pattern: a topic/fanout exchange plus one
// anonymous queue per subscriber) instead of a work queue, and is what
// lets notification-service scale horizontally (per CLAUDE.md's
// architecture goals) without silently dropping most events from each
// client's point of view. The queue is non-durable and auto-deletes
// with the connection: there's nothing to clean up on restart, and
// nothing worth persisting -- see ledger-service's
// internal/ledger/events.go for why this pipeline has no durability
// guarantee to begin with.
//
// # Never gives up, never crashes
//
// Run reconnects with jittered exponential backoff (backoff.go)
// whenever the connection drops or can't be established, for as long
// as ctx is alive. It never returns early because RabbitMQ is down --
// that would either crash the process (if Run's caller treated an
// error as fatal) or silently stop consuming forever, and this
// service's whole job depends on eventually reconnecting once RabbitMQ
// comes back. Ping reports current connectivity truthfully in the
// meantime (see internal/handler.Health), so an orchestrator can still
// tell a replica that's mid-outage apart from one serving normally,
// without this package ever needing to exit to communicate that.
type Consumer struct {
	url     string
	onEvent func(TransactionPostedEvent)

	mu   sync.RWMutex
	conn *amqp.Connection
}

// NewConsumer returns a Consumer that will call onEvent for each
// successfully decoded event once Run is started. It does not connect
// yet -- see Run.
func NewConsumer(url string, onEvent func(TransactionPostedEvent)) *Consumer {
	return &Consumer{url: url, onEvent: onEvent}
}

// Ping reports whether the Consumer currently believes it's connected
// to RabbitMQ. Unlike ledger-service's Postgres health check or
// wallet-service's Redis health check, this doesn't make a network
// round trip of its own: AMQP connections are kept alive by a
// continuous background heartbeat built into the protocol itself
// (unlike a simple request/response ping), so asking the connection
// whether it currently considers itself open is already an accurate,
// near-real-time answer -- not a stale cached flag.
func (c *Consumer) Ping(ctx context.Context) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()

	if conn == nil || conn.IsClosed() {
		return fmt.Errorf("not connected to rabbitmq")
	}
	return nil
}

func (c *Consumer) setConn(conn *amqp.Connection) {
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
}

// Run connects to RabbitMQ and consumes events until ctx is canceled,
// reconnecting with backoff whenever the connection drops or can't be
// established. It always returns once ctx is done, and never
// otherwise -- see the package doc comment.
func (c *Consumer) Run(ctx context.Context) {
	attempt := 0
	for ctx.Err() == nil {
		connected, err := c.consumeUntilError(ctx)
		c.setConn(nil)

		if err != nil && ctx.Err() == nil {
			log.Printf("notification: rabbitmq connection lost, reconnecting: %v", err)
		}

		if connected {
			// However long that connection lasted, it was a real
			// success -- don't let a broker that drops connections
			// after a while (but comes back up fine) make backoff grow
			// without bound across many short-lived connections.
			attempt = 0
		} else {
			attempt++
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff(attempt)):
		}
	}
}

// consumeUntilError dials RabbitMQ, declares the exchange and this
// consumer's private queue (see the package doc comment), and consumes
// from it until ctx is canceled or something fails. connected is true
// if it got far enough to start consuming, even if it later failed --
// Run uses that to decide whether to reset its backoff counter.
func (c *Consumer) consumeUntilError(ctx context.Context) (connected bool, err error) {
	conn, err := amqp.DialConfig(c.url, amqp.Config{Dial: amqp.DefaultDial(dialTimeout)})
	if err != nil {
		return false, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return false, fmt.Errorf("open channel: %w", err)
	}
	defer ch.Close()

	if err := ch.ExchangeDeclare(ExchangeName, "topic", true, false, false, false, nil); err != nil {
		return false, fmt.Errorf("declare exchange: %w", err)
	}

	// Anonymous (server-assigned name), exclusive, auto-delete: a fresh
	// private queue per connection -- see the package doc comment for
	// why every consumer needs its own rather than sharing one.
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		return false, fmt.Errorf("declare queue: %w", err)
	}

	if err := ch.QueueBind(q.Name, RoutingKeyTransactionPosted, ExchangeName, false, nil); err != nil {
		return false, fmt.Errorf("bind queue: %w", err)
	}

	// autoAck=true: this queue dies with the connection regardless (see
	// above), so there is no redelivery-to-self scenario manual acking
	// would protect against here -- only redelivery to a *different*
	// consumer, which doesn't apply to a queue nothing else is bound
	// to. Simpler, and consistent with this pipeline having no
	// durability guarantee to begin with.
	msgs, err := ch.ConsumeWithContext(ctx, q.Name, "", true, true, false, false, nil)
	if err != nil {
		return false, fmt.Errorf("consume: %w", err)
	}

	c.setConn(conn)
	log.Printf("notification: connected to rabbitmq, consuming queue %s", q.Name)

	for {
		select {
		case <-ctx.Done():
			return true, nil
		case d, ok := <-msgs:
			if !ok {
				return true, fmt.Errorf("delivery channel closed")
			}
			c.handleDelivery(d)
		}
	}
}

// handleDelivery decodes one message and calls onEvent. A single
// malformed message (wrong shape, or from some future incompatible
// event type on the same exchange) is logged and dropped -- it must
// never take down the whole consumer, since every other client's live
// feed depends on this same connection staying up.
func (c *Consumer) handleDelivery(d amqp.Delivery) {
	var evt TransactionPostedEvent
	if err := json.Unmarshal(d.Body, &evt); err != nil {
		log.Printf("notification: dropping malformed event: %v", err)
		return
	}
	c.onEvent(evt)
}
