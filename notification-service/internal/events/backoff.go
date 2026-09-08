package events

import (
	"math/rand/v2"
	"time"
)

// Tuning constants for Consumer.Run's reconnect loop. Mirrors the
// shape of ledger-service's internal/ledger.retryBackoff (jittered
// exponential, capped) for the same reason -- see that function's doc
// comment -- applied here to network reconnects instead of database
// row-lock retries: a broker outage that takes down every
// notification-service replica at once should not have all of them
// hammering RabbitMQ with reconnect attempts in lockstep the instant
// it comes back.
const (
	backoffBaseDelay   = 500 * time.Millisecond
	backoffMaxDelay    = 10 * time.Second
	backoffMaxDoubling = 5 // 500ms * 2^5 = 16s, already above backoffMaxDelay
)

// backoff returns how long to wait before reconnect attempt number
// attempt+1, as jittered exponential backoff capped at backoffMaxDelay.
func backoff(attempt int) time.Duration {
	doublings := attempt
	if doublings > backoffMaxDoubling {
		doublings = backoffMaxDoubling
	}
	d := backoffBaseDelay * time.Duration(1<<uint(doublings))
	if d > backoffMaxDelay {
		d = backoffMaxDelay
	}
	return d/2 + rand.N(d/2+1)
}
