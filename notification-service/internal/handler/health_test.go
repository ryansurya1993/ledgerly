package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct {
	err error
}

func (p fakePinger) Ping(ctx context.Context) error { return p.err }

func TestHealth_RabbitMQUp(t *testing.T) {
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
	if got["status"] != "ok" {
		t.Errorf("status = %q, want ok", got["status"])
	}
}

func TestHealth_RabbitMQDownReturns503(t *testing.T) {
	// Unlike wallet-service's Redis (an optional latency optimization),
	// RabbitMQ is this service's only reason to exist -- see Health's
	// doc comment for why that makes 503 the right answer here, same
	// as ledger-service's Postgres check.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	Health(fakePinger{err: errors.New("not connected to rabbitmq")})(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d when RabbitMQ is unreachable", w.Code, http.StatusServiceUnavailable)
	}
	var got map[string]string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != "unavailable" {
		t.Errorf("status = %q, want unavailable", got["status"])
	}
}
