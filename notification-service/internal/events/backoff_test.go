package events

import (
	"testing"
	"time"
)

func TestBackoffStaysWithinBounds(t *testing.T) {
	for attempt := 0; attempt < 10; attempt++ {
		d := backoff(attempt)
		if d < 0 {
			t.Fatalf("backoff(%d) = %v, want >= 0", attempt, d)
		}
		if d > backoffMaxDelay {
			t.Fatalf("backoff(%d) = %v, want <= %v", attempt, d, backoffMaxDelay)
		}
	}
}

func TestBackoffIsJittered(t *testing.T) {
	// Not a proof of randomness, just a guard against a regression that
	// makes backoff a fixed, non-jittered duration -- see the "thundering
	// herd" comment on backoff.go for why that matters when many
	// replicas reconnect after the same outage.
	seen := map[time.Duration]bool{}
	for i := 0; i < 20; i++ {
		seen[backoff(3)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("backoff(3) returned the same value %d times in a row; expected jitter", 20)
	}
}
