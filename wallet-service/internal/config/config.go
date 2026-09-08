// Package config reads wallet-service's runtime configuration from the
// environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds everything wallet-service needs to start: where to
// listen, how to reach ledger-service, and how to reach Redis for the
// fast-path idempotency cache (see internal/idempotency).
type Config struct {
	Port string

	// LedgerServiceURL is the base URL of ledger-service's API.
	// wallet-service never touches Postgres directly -- see
	// CLAUDE.md's architecture section -- so every read or write of
	// ledger state goes through this.
	LedgerServiceURL string

	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// IdempotencyTTL controls how long a cached top-up/transfer
	// response is kept in Redis -- see internal/idempotency's package
	// doc for why letting it expire is safe.
	IdempotencyTTL time.Duration
}

// Load reads Config from the environment.
//
// Unlike ledger-service/internal/db.LoadConfig (which fails fast if its
// DB passwords are missing, because Postgres is a hard dependency it
// cannot function without), nothing here is required: RedisPassword
// defaults to empty (a local/dev Redis typically has no auth), and a
// misconfigured or unreachable Redis is a runtime, per-request
// degradation, not a startup failure -- see internal/idempotency's
// package doc. This is a deliberate consequence of Redis being an
// optional fast path here, not an oversight.
func Load() (Config, error) {
	cfg := Config{
		Port:             getenv("PORT", "8081"),
		LedgerServiceURL: getenv("LEDGER_SERVICE_URL", "http://localhost:8080"),
		RedisAddr:        getenv("WALLET_REDIS_ADDR", "localhost:6379"),
		RedisPassword:    os.Getenv("WALLET_REDIS_PASSWORD"),
	}

	redisDB, err := strconv.Atoi(getenv("WALLET_REDIS_DB", "0"))
	if err != nil {
		return Config{}, fmt.Errorf("WALLET_REDIS_DB must be an integer: %w", err)
	}
	cfg.RedisDB = redisDB

	ttlHours, err := strconv.Atoi(getenv("WALLET_IDEMPOTENCY_TTL_HOURS", "24"))
	if err != nil {
		return Config{}, fmt.Errorf("WALLET_IDEMPOTENCY_TTL_HOURS must be an integer: %w", err)
	}
	cfg.IdempotencyTTL = time.Duration(ttlHours) * time.Hour

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
