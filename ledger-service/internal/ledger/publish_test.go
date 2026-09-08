package ledger

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// fakePublisher is a scriptable EventPublisher for testing
// PostTransaction's publish behavior in isolation, without needing a
// real RabbitMQ -- what's under test here is *when* PostTransaction
// calls its publisher and how it handles that call failing, not
// Publisher's own RabbitMQ wire-up (see internal/events for that).
type fakePublisher struct {
	mu    sync.Mutex
	calls []fakePublishCall
	err   error // returned by every PublishTransactionPosted call, if set
}

type fakePublishCall struct {
	Transaction Transaction
	Entries     []Entry
}

func (f *fakePublisher) PublishTransactionPosted(ctx context.Context, txn Transaction, entries []Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakePublishCall{Transaction: txn, Entries: entries})
	return f.err
}

func (f *fakePublisher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newTestLedgerWithPublisher is newTestLedger, but wired to pub so
// tests can observe what PostTransaction published.
func newTestLedgerWithPublisher(t *testing.T, pub EventPublisher) *Ledger {
	t.Helper()
	return New(requireTestDB(t), pub)
}

func TestPostTransaction_PublishesOnSuccess(t *testing.T) {
	pub := &fakePublisher{}
	l := newTestLedgerWithPublisher(t, pub)
	ctx := context.Background()

	wallet := mustCreateWallet(t, l, "TestPostTransaction_PublishesOnSuccess")
	result, err := l.PostTransaction(ctx, PostTransactionParams{
		IdempotencyKey:  uuid.NewString(),
		TransactionType: TransactionTypeTopUp,
		Description:     "publish test",
		Legs: []Leg{
			{AccountID: ExternalFundingAccountID, Direction: Debit, Amount: 500},
			{AccountID: wallet.ID, Direction: Credit, Amount: 500},
		},
	})
	if err != nil {
		t.Fatalf("PostTransaction: %v", err)
	}

	if got := pub.callCount(); got != 1 {
		t.Fatalf("publisher called %d times, want exactly 1", got)
	}

	call := pub.calls[0]
	if call.Transaction.ID != result.Transaction.ID {
		t.Errorf("published transaction ID = %s, want %s", call.Transaction.ID, result.Transaction.ID)
	}
	if len(call.Entries) != len(result.Entries) {
		t.Errorf("published %d entries, want %d", len(call.Entries), len(result.Entries))
	}
}

func TestPostTransaction_DoesNotPublishOnReplay(t *testing.T) {
	pub := &fakePublisher{}
	l := newTestLedgerWithPublisher(t, pub)
	ctx := context.Background()

	wallet := mustCreateWallet(t, l, "TestPostTransaction_DoesNotPublishOnReplay")
	params := PostTransactionParams{
		IdempotencyKey:  uuid.NewString(),
		TransactionType: TransactionTypeTopUp,
		Legs: []Leg{
			{AccountID: ExternalFundingAccountID, Direction: Debit, Amount: 500},
			{AccountID: wallet.ID, Direction: Credit, Amount: 500},
		},
	}

	if _, err := l.PostTransaction(ctx, params); err != nil {
		t.Fatalf("first PostTransaction: %v", err)
	}
	if got := pub.callCount(); got != 1 {
		t.Fatalf("after first (fresh) call, publisher called %d times, want 1", got)
	}

	result, err := l.PostTransaction(ctx, params)
	if err != nil {
		t.Fatalf("second (replayed) PostTransaction: %v", err)
	}
	if !result.Replayed {
		t.Fatalf("second call with same idempotency key reported Replayed = false")
	}

	if got := pub.callCount(); got != 1 {
		t.Fatalf("after replayed call, publisher called %d times, want still 1 (no new publish)", got)
	}
}

func TestPostTransaction_SucceedsEvenIfPublishFails(t *testing.T) {
	pub := &fakePublisher{err: errors.New("simulated rabbitmq outage")}
	l := newTestLedgerWithPublisher(t, pub)
	ctx := context.Background()

	wallet := mustCreateWallet(t, l, "TestPostTransaction_SucceedsEvenIfPublishFails")
	result, err := l.PostTransaction(ctx, PostTransactionParams{
		IdempotencyKey:  uuid.NewString(),
		TransactionType: TransactionTypeTopUp,
		Legs: []Leg{
			{AccountID: ExternalFundingAccountID, Direction: Debit, Amount: 700},
			{AccountID: wallet.ID, Direction: Credit, Amount: 700},
		},
	})
	if err != nil {
		t.Fatalf("PostTransaction returned an error because publishing failed, want nil: %v", err)
	}
	if result.Replayed {
		t.Fatalf("result.Replayed = true, want false")
	}

	// The write itself must still be fully effective -- a failed
	// notification must never mean a failed (or partially applied)
	// transaction.
	balance, err := l.GetBalance(ctx, wallet.ID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 700 {
		t.Fatalf("wallet balance = %d, want 700 (publish failure must not affect the write)", balance)
	}

	// The publish must still have been *attempted*, not silently
	// skipped just because it was going to fail.
	if got := pub.callCount(); got != 1 {
		t.Fatalf("publisher called %d times, want exactly 1 (attempted despite configured failure)", got)
	}
}
