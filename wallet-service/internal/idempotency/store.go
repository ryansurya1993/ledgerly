// Package idempotency implements wallet-service's fast-path idempotency
// cache: a Redis-backed lookup that lets a retried top-up or transfer
// request skip calling ledger-service entirely and return the original
// response immediately, keyed by the client-supplied idempotency key.
//
// This is explicitly a latency optimization, not the correctness
// guarantee -- see CLAUDE.md ("Idempotency keys are stored in Redis...
// never in local/in-memory state") and ledger-service/README.md's notes
// on transactions.idempotency_key's UNIQUE constraint. If Redis is
// unreachable, evicts a key before this package's TTL expires it, or
// two concurrent requests both miss the cache at the same instant and
// both proceed to call ledger-service, the request still ends up
// correct: ledger-service's UNIQUE constraint on idempotency_key is
// what actually prevents the same logical operation from being applied
// twice, and it returns the original transaction (with Replayed: true)
// to whichever caller loses that race. Every method here is meant to be
// used "best effort": a Get error is treated by callers as a cache miss
// (fall through to ledger-service), and a Set error is logged and
// ignored, never surfaced to the client -- see
// internal/handler/wallets.go.
package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// keyPrefix namespaces this package's keys, in case the Redis instance
// wallet-service is pointed at is ever shared with another use.
const keyPrefix = "wallet-service:idempotency:"

// CachedResponse is what gets stored for a given idempotency key --
// enough to reproduce the original HTTP response exactly on replay,
// without wallet-service needing to call ledger-service again or
// recompute anything.
type CachedResponse struct {
	StatusCode int             `json:"status_code"`
	Body       json.RawMessage `json:"body"`
}

// Store is a Redis-backed idempotency cache.
type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

// New wraps rdb. ttl controls how long a cached response is kept --
// once it expires, a retried request simply falls through to
// ledger-service again, which is safe (see the package doc comment).
func New(rdb *redis.Client, ttl time.Duration) *Store {
	return &Store{rdb: rdb, ttl: ttl}
}

// Get returns the cached response for key, if any. It does not
// distinguish "key genuinely isn't cached" from "couldn't reach Redis
// to check" in its found return value -- callers only need err to
// decide whether to log a warning, since both cases mean the same
// thing operationally: fall through to calling ledger-service.
func (s *Store) Get(ctx context.Context, key string) (resp CachedResponse, found bool, err error) {
	raw, err := s.rdb.Get(ctx, keyPrefix+key).Bytes()
	if errors.Is(err, redis.Nil) {
		return CachedResponse{}, false, nil
	}
	if err != nil {
		return CachedResponse{}, false, fmt.Errorf("redis get: %w", err)
	}

	if err := json.Unmarshal(raw, &resp); err != nil {
		return CachedResponse{}, false, fmt.Errorf("unmarshal cached response: %w", err)
	}
	return resp, true, nil
}

// Set caches resp under key with this Store's configured TTL. Per the
// package doc comment, callers should treat a returned error as
// non-fatal: log it and continue, since the request has already
// completed correctly by the time Set is called.
func (s *Store) Set(ctx context.Context, key string, resp CachedResponse) error {
	raw, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response to cache: %w", err)
	}
	if err := s.rdb.Set(ctx, keyPrefix+key, raw, s.ttl).Err(); err != nil {
		return fmt.Errorf("redis set: %w", err)
	}
	return nil
}
