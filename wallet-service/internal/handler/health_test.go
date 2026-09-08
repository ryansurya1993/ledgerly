package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct {
	err error
}

func (p fakePinger) Ping(ctx context.Context) error { return p.err }

func TestHealth_RedisUp(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	Health(fakePinger{})(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got map[string]string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["redis"] != "ok" {
		t.Errorf("redis = %q, want ok", got["redis"])
	}
}

func TestHealth_RedisDownStillReturns200(t *testing.T) {
	// Redis is an optional fast path (see internal/idempotency's
	// package doc) -- an unreachable Redis must not turn this into a
	// 503 and pull a perfectly-functional replica out of rotation.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	Health(fakePinger{err: errPlain("connection refused")})(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d even when Redis is unreachable", w.Code, http.StatusOK)
	}
	var got map[string]string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != "ok" {
		t.Errorf("status = %q, want ok", got["status"])
	}
	if got["redis"] != "unavailable" {
		t.Errorf("redis = %q, want unavailable", got["redis"])
	}
}
