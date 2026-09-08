package idempotency

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestGet_Miss(t *testing.T) {
	rdb := requireTestRedis(t)
	store := New(rdb, time.Hour)

	_, found, err := store.Get(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("found = true for a key that was never set")
	}
}

func TestSetThenGet_RoundTrips(t *testing.T) {
	rdb := requireTestRedis(t)
	store := New(rdb, time.Hour)
	ctx := context.Background()
	key := uuid.NewString()

	want := CachedResponse{
		StatusCode: 201,
		Body:       json.RawMessage(`{"transaction_id":"txn-1","amount":500}`),
	}
	if err := store.Set(ctx, key, want); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, found, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("found = false after Set")
	}
	if got.StatusCode != want.StatusCode {
		t.Errorf("StatusCode = %d, want %d", got.StatusCode, want.StatusCode)
	}
	if string(got.Body) != string(want.Body) {
		t.Errorf("Body = %s, want %s", got.Body, want.Body)
	}
}

func TestGet_DoesNotLeakAcrossKeys(t *testing.T) {
	rdb := requireTestRedis(t)
	store := New(rdb, time.Hour)
	ctx := context.Background()

	keyA, keyB := uuid.NewString(), uuid.NewString()
	if err := store.Set(ctx, keyA, CachedResponse{StatusCode: 201, Body: json.RawMessage(`{"a":1}`)}); err != nil {
		t.Fatalf("Set(keyA): %v", err)
	}

	_, found, err := store.Get(ctx, keyB)
	if err != nil {
		t.Fatalf("Get(keyB): %v", err)
	}
	if found {
		t.Fatal("Get(keyB) found a value cached under a different key")
	}
}

func TestGet_ExpiresAfterTTL(t *testing.T) {
	rdb := requireTestRedis(t)
	// A short TTL so the test doesn't have to wait long -- this
	// exercises the exact mechanism (Redis's own key expiry) that makes
	// it safe for a retried request to fall through to ledger-service
	// once a cached response ages out; see the package doc comment.
	store := New(rdb, 200*time.Millisecond)
	ctx := context.Background()
	key := uuid.NewString()

	if err := store.Set(ctx, key, CachedResponse{StatusCode: 200, Body: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	_, found, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get (before expiry): %v", err)
	}
	if !found {
		t.Fatal("found = false immediately after Set, before TTL elapsed")
	}

	time.Sleep(400 * time.Millisecond)

	_, found, err = store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get (after expiry): %v", err)
	}
	if found {
		t.Fatal("found = true after TTL should have expired the key")
	}
}
