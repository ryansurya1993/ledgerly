package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/ledger"
)

// dialTimeout bounds how long a single (re)connect attempt can take.
// PostTransaction calls Publish synchronously (see
// internal/ledger/events.go), so this is also the worst-case extra
// latency a write request pays when RabbitMQ is unreachable -- short
// enough that a down broker degrades requests, rather than stalling
// them.
const dialTimeout = 2 * time.Second

// Publisher implements ledger.EventPublisher against a real RabbitMQ
// broker. It satisfies that interface structurally (see
// internal/ledger/ledger.go's New) -- internal/ledger never imports
// this package, only the other way around, so the core business logic
// stays decoupled from messaging infrastructure.
//
// Connection handling is deliberately simple, not a persistent
// background reconnect loop: PublishTransactionPosted (re)connects
// lazily, on demand, the next time it's called after the connection is
// found to be down. That fits this package's own best-effort contract
// -- there is no continuous consumption to keep alive here, just an
// occasional publish -- and avoids a goroutine that would otherwise run
// for the life of the process for a dependency that might never be
// used. One accepted consequence, appropriate for this project's
// scope: if RabbitMQ is down for an extended period, every
// PostTransaction call pays one dialTimeout-bounded reconnect attempt
// rather than backing off: a production system handling enough
// traffic for that cost to matter would add a circuit breaker (stop
// attempting to reconnect for a cooldown window after N consecutive
// failures); see the root README's Scaling Roadmap.
type Publisher struct {
	url string

	mu   sync.Mutex
	conn *amqp.Connection
	ch   *amqp.Channel
}

// NewPublisher returns a Publisher for cfg.URL. It attempts an initial
// connection but does not fail if that attempt doesn't succeed --
// RabbitMQ being unreachable at startup is not fatal here (unlike
// Postgres in cmd/ledger-service/main.go); the first real
// PublishTransactionPosted call will try again. The returned error is
// purely informational, for a startup log line; callers are not
// expected to check it.
func NewPublisher(cfg Config) (*Publisher, error) {
	p := &Publisher{url: cfg.URL}
	err := p.connect()
	return p, err
}

// connect dials RabbitMQ, opens a channel, and declares ExchangeName.
// Declaring here (rather than assuming it already exists) means
// ledger-service works whether it starts before or after
// notification-service -- exchange declaration is idempotent as long
// as every declaration uses the same parameters, which is why
// ExchangeName's doc comment insists both sides stay in sync. Callers
// must hold p.mu.
func (p *Publisher) connect() error {
	conn, err := amqp.DialConfig(p.url, amqp.Config{Dial: amqp.DefaultDial(dialTimeout)})
	if err != nil {
		return fmt.Errorf("dial rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("open channel: %w", err)
	}

	if err := ch.ExchangeDeclare(ExchangeName, "topic", true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("declare exchange: %w", err)
	}

	p.conn = conn
	p.ch = ch
	return nil
}

// PublishTransactionPosted implements ledger.EventPublisher.
func (p *Publisher) PublishTransactionPosted(ctx context.Context, txn ledger.Transaction, entries []ledger.Entry) error {
	evt := TransactionPostedEvent{
		TransactionID:   txn.ID.String(),
		TransactionType: string(txn.TransactionType),
		Description:     txn.Description,
		Legs:            make([]EventLeg, len(entries)),
		CreatedAt:       txn.CreatedAt,
	}
	for i, e := range entries {
		evt.Legs[i] = EventLeg{
			AccountID: e.AccountID.String(),
			Direction: string(e.Direction),
			Amount:    e.Amount,
		}
	}

	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ch == nil || p.conn == nil || p.conn.IsClosed() {
		if err := p.connect(); err != nil {
			return err
		}
	}

	err = p.ch.PublishWithContext(ctx, ExchangeName, RoutingKeyTransactionPosted, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now(),
		Body:         body,
	})
	if err != nil {
		// The channel or connection is presumed dead; drop both so the
		// next call redials from scratch rather than repeatedly trying
		// a channel we already know is broken.
		p.ch = nil
		if p.conn != nil {
			p.conn.Close()
			p.conn = nil
		}
		return fmt.Errorf("publish: %w", err)
	}

	return nil
}

// Close releases the connection, if one is open. Safe to call even if
// NewPublisher's initial connect failed. Errors are ignored -- this
// only ever runs during process shutdown, where there is nothing
// meaningful left to do with a close failure.
func (p *Publisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ch != nil {
		p.ch.Close()
	}
	if p.conn != nil {
		p.conn.Close()
	}
}
