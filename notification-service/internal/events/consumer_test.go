package events

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// publishRaw declares ExchangeName (idempotent -- safe even if the
// consumer under test already declared it) and publishes body under
// routingKey, using a fresh connection independent of the Consumer
// under test.
func publishRaw(ctx context.Context, url, routingKey string, body []byte) error {
	conn, err := amqp.DialConfig(url, amqp.Config{Dial: amqp.DefaultDial(2 * time.Second)})
	if err != nil {
		return err
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()

	if err := ch.ExchangeDeclare(ExchangeName, "topic", true, false, false, false, nil); err != nil {
		return err
	}

	return ch.PublishWithContext(ctx, ExchangeName, routingKey, false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
	})
}

func publishEvent(ctx context.Context, url string, evt TransactionPostedEvent) error {
	body, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	return publishRaw(ctx, url, RoutingKeyTransactionPosted, body)
}

// waitForPing blocks until c.Ping's connected/disconnected state
// matches want, or fails t after timeout. Polling is necessary because
// Run connects (and reconnects) on its own goroutine, on its own
// schedule.
func waitForPing(t *testing.T, c *Consumer, want bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		connected := c.Ping(context.Background()) == nil
		if connected == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Consumer connected=%v not reached within %v", want, timeout)
}

// eventRecorder collects events an onEvent callback receives, safe for
// the consumer goroutine to write to concurrently with the test
// goroutine reading it.
type eventRecorder struct {
	mu     sync.Mutex
	events []TransactionPostedEvent
}

func (r *eventRecorder) record(evt TransactionPostedEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, evt)
}

func (r *eventRecorder) snapshot() []TransactionPostedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TransactionPostedEvent, len(r.events))
	copy(out, r.events)
	return out
}

func sampleEvent(transactionID string) TransactionPostedEvent {
	return TransactionPostedEvent{
		TransactionID:   transactionID,
		TransactionType: "top_up",
		Description:     "test event",
		Legs: []EventLeg{
			{AccountID: "11111111-1111-1111-1111-111111111111", Direction: "debit", Amount: 500},
			{AccountID: "22222222-2222-2222-2222-222222222222", Direction: "credit", Amount: 500},
		},
		CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
}

func TestConsumer_DeliversPublishedEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rmq, stop, err := startRabbitMQ(ctx)
	if err != nil {
		t.Skipf("no test RabbitMQ available (Docker missing or failed to start): %v", err)
	}
	defer stop()

	rec := &eventRecorder{}
	c := NewConsumer(rmq.url, rec.record)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go c.Run(runCtx)

	waitForPing(t, c, true, 15*time.Second)

	want := sampleEvent("tx-delivers-published-events")
	if err := publishEvent(ctx, rmq.url, want); err != nil {
		t.Fatalf("publishEvent: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events, want 1", len(got))
	}
	if got[0].TransactionID != want.TransactionID {
		t.Errorf("TransactionID = %q, want %q", got[0].TransactionID, want.TransactionID)
	}
	if len(got[0].Legs) != 2 {
		t.Errorf("len(Legs) = %d, want 2", len(got[0].Legs))
	}
}

func TestConsumer_IgnoresMalformedMessageThenDeliversNextValidOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rmq, stop, err := startRabbitMQ(ctx)
	if err != nil {
		t.Skipf("no test RabbitMQ available (Docker missing or failed to start): %v", err)
	}
	defer stop()

	rec := &eventRecorder{}
	c := NewConsumer(rmq.url, rec.record)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go c.Run(runCtx)

	waitForPing(t, c, true, 15*time.Second)

	if err := publishRaw(ctx, rmq.url, RoutingKeyTransactionPosted, []byte("not valid json at all")); err != nil {
		t.Fatalf("publishRaw(malformed): %v", err)
	}

	want := sampleEvent("tx-after-malformed-message")
	if err := publishEvent(ctx, rmq.url, want); err != nil {
		t.Fatalf("publishEvent: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events, want exactly 1 (the malformed message must be dropped, not delivered, and must not kill the consumer)", len(got))
	}
	if got[0].TransactionID != want.TransactionID {
		t.Errorf("TransactionID = %q, want %q", got[0].TransactionID, want.TransactionID)
	}
}

func TestConsumer_PingReflectsConnectivity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rmq, stop, err := startRabbitMQ(ctx)
	if err != nil {
		t.Skipf("no test RabbitMQ available (Docker missing or failed to start): %v", err)
	}
	defer stop()

	c := NewConsumer(rmq.url, func(TransactionPostedEvent) {})

	if err := c.Ping(context.Background()); err == nil {
		t.Fatalf("Ping() before Run() = nil, want an error (not connected yet)")
	}

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go c.Run(runCtx)

	waitForPing(t, c, true, 15*time.Second)
}

// TestConsumer_ReconnectsAfterBrokerRestart is the capstone proof of
// this package's central resilience claim: a Consumer survives its
// broker disappearing and coming back, without crashing and without
// needing anything external to restart it -- see Run's doc comment.
// This is slower than the other tests here (it really stops and starts
// a container) but the behavior it's protecting is explicitly required
// -- see notification-service/README.md -- and easy to silently break
// in a refactor of the reconnect loop.
func TestConsumer_ReconnectsAfterBrokerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	rmq, stop, err := startRabbitMQ(ctx)
	if err != nil {
		t.Skipf("no test RabbitMQ available (Docker missing or failed to start): %v", err)
	}
	defer stop()

	rec := &eventRecorder{}
	c := NewConsumer(rmq.url, rec.record)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go c.Run(runCtx)

	waitForPing(t, c, true, 15*time.Second)

	// kill and revive are separate steps (not one blocking "restart")
	// specifically so this assertion is deterministic: checked right
	// after the broker disappears and before any replacement exists,
	// rather than after revive (which can itself take several seconds)
	// returns -- by which point the Consumer could easily have already
	// reconnected on its own, making a post-hoc "was it ever
	// disconnected" check racy. See kill's doc comment.
	rmq.kill()
	waitForPing(t, c, false, 15*time.Second)

	if err := rmq.revive(ctx); err != nil {
		t.Fatalf("revive rabbitmq container: %v", err)
	}
	waitForPing(t, c, true, 30*time.Second)

	want := sampleEvent("tx-after-broker-restart")
	if err := publishEvent(ctx, rmq.url, want); err != nil {
		t.Fatalf("publishEvent after reconnect: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events after reconnect, want 1", len(got))
	}
	if got[0].TransactionID != want.TransactionID {
		t.Errorf("TransactionID = %q, want %q", got[0].TransactionID, want.TransactionID)
	}
}
