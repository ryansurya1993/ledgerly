package ledger

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// newTestLedger returns a Ledger backed by the real, migrated Postgres
// instance TestMain started, skipping t if that instance isn't
// available.
func newTestLedger(t *testing.T) *Ledger {
	t.Helper()
	return New(requireTestDB(t))
}

func mustCreateWallet(t *testing.T, l *Ledger, name string) Account {
	t.Helper()
	a, err := l.CreateAccount(context.Background(), CreateAccountParams{
		Name:        name,
		AccountType: AccountTypeWallet,
	})
	if err != nil {
		t.Fatalf("CreateAccount(%q): %v", name, err)
	}
	return a
}

func mustTopUp(t *testing.T, l *Ledger, accountID uuid.UUID, amount int64) {
	t.Helper()
	_, err := l.PostTransaction(context.Background(), PostTransactionParams{
		IdempotencyKey:  uuid.NewString(),
		TransactionType: TransactionTypeTopUp,
		Description:     "test top-up",
		Legs: []Leg{
			{AccountID: ExternalFundingAccountID, Direction: Debit, Amount: amount},
			{AccountID: accountID, Direction: Credit, Amount: amount},
		},
	})
	if err != nil {
		t.Fatalf("top-up %s by %d: %v", accountID, amount, err)
	}
}

func transferParams(from, to uuid.UUID, amount int64, key string) PostTransactionParams {
	return PostTransactionParams{
		IdempotencyKey:  key,
		TransactionType: TransactionTypeTransfer,
		Description:     "test transfer",
		Legs: []Leg{
			{AccountID: from, Direction: Debit, Amount: amount},
			{AccountID: to, Direction: Credit, Amount: amount},
		},
	}
}

func TestCreateAccountAndGetBalance(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	a := mustCreateWallet(t, l, "TestCreateAccountAndGetBalance")
	if a.Balance != 0 {
		t.Fatalf("new account balance = %d, want 0", a.Balance)
	}
	if a.Version != 0 {
		t.Fatalf("new account version = %d, want 0", a.Version)
	}

	balance, err := l.GetBalance(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 0 {
		t.Fatalf("GetBalance = %d, want 0", balance)
	}
}

func TestGetBalance_UnknownAccount(t *testing.T) {
	l := newTestLedger(t)
	_, err := l.GetBalance(context.Background(), uuid.New())
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("GetBalance(unknown) = %v, want ErrAccountNotFound", err)
	}
}

func TestPostTransaction_TopUp(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	wallet := mustCreateWallet(t, l, "TestPostTransaction_TopUp")

	result, err := l.PostTransaction(ctx, PostTransactionParams{
		IdempotencyKey:  uuid.NewString(),
		TransactionType: TransactionTypeTopUp,
		Legs: []Leg{
			{AccountID: ExternalFundingAccountID, Direction: Debit, Amount: 500},
			{AccountID: wallet.ID, Direction: Credit, Amount: 500},
		},
	})
	if err != nil {
		t.Fatalf("PostTransaction: %v", err)
	}
	if result.Replayed {
		t.Fatalf("first call reported Replayed = true")
	}
	if len(result.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2", len(result.Entries))
	}

	balance, err := l.GetBalance(ctx, wallet.ID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 500 {
		t.Fatalf("wallet balance = %d, want 500", balance)
	}

	integrity, err := l.CheckAccountIntegrity(ctx, wallet.ID)
	if err != nil {
		t.Fatalf("CheckAccountIntegrity: %v", err)
	}
	if integrity.Drifted() {
		t.Fatalf("wallet integrity drifted: cached=%d computed=%d", integrity.CachedBalance, integrity.ComputedBalance)
	}
}

func TestPostTransaction_InsufficientFunds(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	from := mustCreateWallet(t, l, "TestPostTransaction_InsufficientFunds_from")
	to := mustCreateWallet(t, l, "TestPostTransaction_InsufficientFunds_to")

	_, err := l.PostTransaction(ctx, transferParams(from.ID, to.ID, 100, uuid.NewString()))
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("PostTransaction from empty wallet = %v, want ErrInsufficientFunds", err)
	}

	// A rejected transfer must not have moved anything.
	balance, err := l.GetBalance(ctx, to.ID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 0 {
		t.Fatalf("destination balance = %d after rejected transfer, want 0", balance)
	}
}

func TestPostTransaction_InvalidLegsRejectedBeforeTouchingDB(t *testing.T) {
	l := newTestLedger(t)
	_, err := l.PostTransaction(context.Background(), PostTransactionParams{
		IdempotencyKey:  uuid.NewString(),
		TransactionType: TransactionTypeTransfer,
		Legs: []Leg{
			{AccountID: uuid.New(), Direction: Debit, Amount: 100},
			{AccountID: uuid.New(), Direction: Credit, Amount: 50}, // unbalanced
		},
	})
	if !errors.Is(err, ErrInvalidLegs) {
		t.Fatalf("PostTransaction with unbalanced legs = %v, want ErrInvalidLegs", err)
	}
}

func TestPostTransaction_IdempotentReplay(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	from := mustCreateWallet(t, l, "TestPostTransaction_IdempotentReplay_from")
	to := mustCreateWallet(t, l, "TestPostTransaction_IdempotentReplay_to")
	mustTopUp(t, l, from.ID, 1000)

	key := uuid.NewString()
	first, err := l.PostTransaction(ctx, transferParams(from.ID, to.ID, 200, key))
	if err != nil {
		t.Fatalf("first PostTransaction: %v", err)
	}
	if first.Replayed {
		t.Fatalf("first call reported Replayed = true")
	}

	second, err := l.PostTransaction(ctx, transferParams(from.ID, to.ID, 200, key))
	if err != nil {
		t.Fatalf("second (replayed) PostTransaction: %v", err)
	}
	if !second.Replayed {
		t.Fatalf("second call with same idempotency key reported Replayed = false")
	}
	if second.Transaction.ID != first.Transaction.ID {
		t.Fatalf("replayed transaction ID = %s, want %s", second.Transaction.ID, first.Transaction.ID)
	}

	// The critical assertion: replaying the same key must not move money
	// twice.
	toBalance, err := l.GetBalance(ctx, to.ID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if toBalance != 200 {
		t.Fatalf("destination balance after replay = %d, want 200 (transfer applied once)", toBalance)
	}
}

func TestGetHistory_ChronologicalOrder(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	a := mustCreateWallet(t, l, "TestGetHistory_ChronologicalOrder")
	mustTopUp(t, l, a.ID, 100)
	mustTopUp(t, l, a.ID, 200)
	mustTopUp(t, l, a.ID, 300)

	history, err := l.GetHistory(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("len(history) = %d, want 3", len(history))
	}

	wantAmounts := []int64{100, 200, 300}
	for i, h := range history {
		if h.Amount != wantAmounts[i] {
			t.Fatalf("history[%d].Amount = %d, want %d (order: %v)", i, h.Amount, wantAmounts[i], history)
		}
		if h.Direction != Credit {
			t.Fatalf("history[%d].Direction = %s, want credit", i, h.Direction)
		}
		if h.TransactionType != TransactionTypeTopUp {
			t.Fatalf("history[%d].TransactionType = %s, want top_up", i, h.TransactionType)
		}
	}
	for i := 1; i < len(history); i++ {
		if history[i].CreatedAt.Before(history[i-1].CreatedAt) {
			t.Fatalf("history not in chronological order: entry %d created before entry %d", i, i-1)
		}
	}
}

// TestConcurrentTransfersSameAccount is the test the ledger's
// correctness claim rests on: fire many concurrent transfers that all
// debit the same source account, and confirm the final balance is
// exactly right -- no lost updates, no double-applied updates, despite
// every goroutine racing to update the same accounts row.
//
// See the "Concurrency" section of PostTransaction's doc comment
// (post_transaction.go) for the mechanism this is exercising: each
// goroutine's PostTransaction call independently runs the
// read-version / conditional-UPDATE / retry-on-conflict loop, so this
// test is expected to trigger real version-conflict retries under load,
// not just succeed on the first attempt every time.
func TestConcurrentTransfersSameAccount(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	const (
		numTransfers = 100
		amount       = int64(10)
		startBalance = int64(50_000)
	)

	from := mustCreateWallet(t, l, "TestConcurrentTransfersSameAccount_from")
	to := mustCreateWallet(t, l, "TestConcurrentTransfersSameAccount_to")
	mustTopUp(t, l, from.ID, startBalance)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	g, gctx := errgroup.WithContext(ctx)
	for i := 0; i < numTransfers; i++ {
		key := fmt.Sprintf("concurrent-same-account-%d-%s", i, uuid.NewString())
		g.Go(func() error {
			_, err := l.PostTransaction(gctx, transferParams(from.ID, to.ID, amount, key))
			return err
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent transfer failed: %v", err)
	}

	wantFrom := startBalance - int64(numTransfers)*amount
	wantTo := int64(numTransfers) * amount

	fromBalance, err := l.GetBalance(ctx, from.ID)
	if err != nil {
		t.Fatalf("GetBalance(from): %v", err)
	}
	if fromBalance != wantFrom {
		t.Fatalf("source balance = %d, want %d (lost or duplicated update)", fromBalance, wantFrom)
	}

	toBalance, err := l.GetBalance(ctx, to.ID)
	if err != nil {
		t.Fatalf("GetBalance(to): %v", err)
	}
	if toBalance != wantTo {
		t.Fatalf("destination balance = %d, want %d (lost or duplicated update)", toBalance, wantTo)
	}

	for _, acct := range []Account{from, to} {
		integrity, err := l.CheckAccountIntegrity(ctx, acct.ID)
		if err != nil {
			t.Fatalf("CheckAccountIntegrity(%s): %v", acct.Name, err)
		}
		if integrity.Drifted() {
			t.Fatalf("account %s drifted after concurrent load: cached=%d computed=%d",
				acct.Name, integrity.CachedBalance, integrity.ComputedBalance)
		}
	}

	history, err := l.GetHistory(ctx, from.ID)
	if err != nil {
		t.Fatalf("GetHistory(from): %v", err)
	}
	if len(history) != numTransfers+1 { // +1 for the initial top-up
		t.Fatalf("len(history) = %d, want %d", len(history), numTransfers+1)
	}
}

// TestConcurrentTransfersOppositeDirections exercises the deadlock
// scenario described in PostTransaction's doc comment directly: half
// the goroutines transfer A->B while the other half transfer B->A, at
// the same time. Without sortedAccountIDs imposing a consistent lock
// order, this exact pattern (two transactions wanting each other's
// locked row) is the textbook way to produce a Postgres
// deadlock_detected error. This test asserts the whole batch completes
// successfully -- if lock ordering were ever removed or broken, this is
// the test expected to start flaking with deadlock errors under load.
func TestConcurrentTransfersOppositeDirections(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	const (
		transfersPerDirection = 50
		amount                = int64(5)
		startBalance          = int64(10_000)
	)

	a := mustCreateWallet(t, l, "TestConcurrentTransfersOppositeDirections_a")
	b := mustCreateWallet(t, l, "TestConcurrentTransfersOppositeDirections_b")
	mustTopUp(t, l, a.ID, startBalance)
	mustTopUp(t, l, b.ID, startBalance)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	g, gctx := errgroup.WithContext(ctx)
	for i := 0; i < transfersPerDirection; i++ {
		i := i
		g.Go(func() error {
			key := fmt.Sprintf("opposite-a-to-b-%d-%s", i, uuid.NewString())
			_, err := l.PostTransaction(gctx, transferParams(a.ID, b.ID, amount, key))
			return err
		})
		g.Go(func() error {
			key := fmt.Sprintf("opposite-b-to-a-%d-%s", i, uuid.NewString())
			_, err := l.PostTransaction(gctx, transferParams(b.ID, a.ID, amount, key))
			return err
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent bidirectional transfer failed: %v", err)
	}

	// Equal amounts moved in both directions, so both accounts should
	// end up exactly back where they started.
	for _, acct := range []Account{a, b} {
		balance, err := l.GetBalance(ctx, acct.ID)
		if err != nil {
			t.Fatalf("GetBalance(%s): %v", acct.Name, err)
		}
		if balance != startBalance {
			t.Fatalf("account %s balance = %d, want %d", acct.Name, balance, startBalance)
		}

		integrity, err := l.CheckAccountIntegrity(ctx, acct.ID)
		if err != nil {
			t.Fatalf("CheckAccountIntegrity(%s): %v", acct.Name, err)
		}
		if integrity.Drifted() {
			t.Fatalf("account %s drifted: cached=%d computed=%d", acct.Name, integrity.CachedBalance, integrity.ComputedBalance)
		}
	}
}

func TestCheckAllAccountsIntegrity(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	a := mustCreateWallet(t, l, "TestCheckAllAccountsIntegrity")
	mustTopUp(t, l, a.ID, 42)

	results, err := l.CheckAllAccountsIntegrity(ctx)
	if err != nil {
		t.Fatalf("CheckAllAccountsIntegrity: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected at least the seeded external funding account plus the one just created")
	}
	for _, r := range results {
		if r.Drifted() {
			t.Fatalf("account %s drifted: cached=%d computed=%d", r.AccountID, r.CachedBalance, r.ComputedBalance)
		}
	}
}
