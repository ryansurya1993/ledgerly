package ledger

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These are pure-function unit tests: no database involved. The
// concurrency-critical, DB-dependent behavior (post_transaction.go's
// retry loop, lock ordering, actual constraint enforcement) is only
// meaningfully tested against a real Postgres -- see integration_test.go.

func TestValidateLegs(t *testing.T) {
	acctA, acctB := uuid.New(), uuid.New()

	tests := []struct {
		name    string
		legs    []Leg
		wantErr error
	}{
		{
			name: "valid transfer",
			legs: []Leg{
				{AccountID: acctA, Direction: Debit, Amount: 100},
				{AccountID: acctB, Direction: Credit, Amount: 100},
			},
			wantErr: nil,
		},
		{
			name:    "too few legs",
			legs:    []Leg{{AccountID: acctA, Direction: Debit, Amount: 100}},
			wantErr: ErrInvalidLegs,
		},
		{
			name: "zero amount",
			legs: []Leg{
				{AccountID: acctA, Direction: Debit, Amount: 0},
				{AccountID: acctB, Direction: Credit, Amount: 0},
			},
			wantErr: ErrInvalidLegs,
		},
		{
			name: "negative amount",
			legs: []Leg{
				{AccountID: acctA, Direction: Debit, Amount: -50},
				{AccountID: acctB, Direction: Credit, Amount: -50},
			},
			wantErr: ErrInvalidLegs,
		},
		{
			name: "unrecognized direction",
			legs: []Leg{
				{AccountID: acctA, Direction: "sideways", Amount: 100},
				{AccountID: acctB, Direction: Credit, Amount: 100},
			},
			wantErr: ErrInvalidLegs,
		},
		{
			name: "unbalanced",
			legs: []Leg{
				{AccountID: acctA, Direction: Debit, Amount: 100},
				{AccountID: acctB, Direction: Credit, Amount: 90},
			},
			wantErr: ErrInvalidLegs,
		},
		{
			name: "three legs still balanced (split payment shape)",
			legs: []Leg{
				{AccountID: acctA, Direction: Debit, Amount: 100},
				{AccountID: acctB, Direction: Credit, Amount: 60},
				{AccountID: uuid.New(), Direction: Credit, Amount: 40},
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLegs(tt.legs)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("validateLegs() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("validateLegs() = %v, want error wrapping %v", err, tt.wantErr)
			}
		})
	}
}

func TestNetDeltas(t *testing.T) {
	acctA, acctB := uuid.New(), uuid.New()

	// Two legs against the same account in one transaction should net
	// into a single delta, not two separate entries in the map --
	// that's what lets applyBalanceDelta issue exactly one UPDATE per
	// account per attempt.
	legs := []Leg{
		{AccountID: acctA, Direction: Debit, Amount: 100},
		{AccountID: acctA, Direction: Credit, Amount: 30},
		{AccountID: acctB, Direction: Credit, Amount: 70},
	}

	deltas := netDeltas(legs)

	if got, want := deltas[acctA], int64(-70); got != want {
		t.Errorf("deltas[acctA] = %d, want %d", got, want)
	}
	if got, want := deltas[acctB], int64(70); got != want {
		t.Errorf("deltas[acctB] = %d, want %d", got, want)
	}
	if len(deltas) != 2 {
		t.Errorf("len(deltas) = %d, want 2", len(deltas))
	}
}

func TestSortedAccountIDsIsOrderIndependent(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	deltas := map[uuid.UUID]int64{a: 1, b: 2, c: 3}

	// The whole point of sortedAccountIDs is that two transactions
	// touching the same accounts always lock them in the same order
	// regardless of which account each caller thinks of as "first"
	// (source vs. destination) -- see the deadlock discussion in
	// post_transaction.go. Calling it twice on the same set must
	// produce identical output.
	first := sortedAccountIDs(deltas)
	second := sortedAccountIDs(deltas)

	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("expected 3 ids, got %d and %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("sortedAccountIDs is not deterministic: %v vs %v", first, second)
		}
	}
}

func TestRetryBackoffStaysWithinBounds(t *testing.T) {
	for attempt := 0; attempt < 10; attempt++ {
		d := retryBackoff(attempt)
		if d < 0 {
			t.Fatalf("retryBackoff(%d) = %v, want >= 0", attempt, d)
		}
		if d > retryMaxDelay {
			t.Fatalf("retryBackoff(%d) = %v, want <= %v", attempt, d, retryMaxDelay)
		}
	}
}

func TestRetryBackoffIsJittered(t *testing.T) {
	// Not a proof of randomness, just a guard against a regression that
	// makes backoff a fixed, non-jittered duration -- see the "thundering
	// herd" comment on retryBackoff for why that matters under
	// contention.
	seen := map[time.Duration]bool{}
	for i := 0; i < 20; i++ {
		seen[retryBackoff(3)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("retryBackoff(3) returned the same value %d times in a row; expected jitter", 20)
	}
}
