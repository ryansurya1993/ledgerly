package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ryansurya1993/ledgerly/wallet-service/internal/config"
	"github.com/ryansurya1993/ledgerly/wallet-service/internal/handler"
	"github.com/ryansurya1993/ledgerly/wallet-service/internal/idempotency"
	"github.com/ryansurya1993/ledgerly/wallet-service/internal/ledgerclient"
)

// redisPinger adapts *redis.Client's Ping (which returns a
// *redis.StatusCmd) to the plain `Ping(ctx) error` shape
// internal/handler.Pinger expects -- mirrors the interface
// ledger-service's health handler takes from *pgxpool.Pool, which
// already returns a plain error and needs no adapter.
type redisPinger struct {
	rdb *redis.Client
}

func (p redisPinger) Ping(ctx context.Context) error {
	return p.rdb.Ping(ctx).Err()
}

// noBrowserCache disables caching for the wrapped handler's responses.
// http.FileServer sends a bare Last-Modified header and nothing else,
// which gives browsers no explicit freshness lifetime to go by -- they
// fall back to a heuristic (commonly a fraction of the time since
// Last-Modified) and can keep serving an old cached copy across a
// plain refresh, only revalidating on a hard reload. That's directly at
// odds with this handler's own doc comment above ("editing
// frontend/*.html|css|js takes effect on refresh"), and this is a local
// demo served straight off disk, not a CDN-fronted production asset,
// so there's no real performance cost to trading away for that
// surprise.
func noBrowserCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// No startup ping against Redis, unlike ledger-service's startup
	// ping against Postgres -- Redis is an optional fast path here (see
	// internal/idempotency's package doc), not a dependency the service
	// needs to be reachable to serve traffic correctly.
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer rdb.Close()

	ledgerSvc := ledgerclient.New(cfg.LedgerServiceURL, nil)
	idemStore := idempotency.New(rdb, cfg.IdempotencyTTL)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health(redisPinger{rdb}))
	mux.HandleFunc("POST /wallets", handler.CreateWallet(ledgerSvc))
	mux.HandleFunc("GET /wallets", handler.ListWallets(ledgerSvc))
	mux.HandleFunc("POST /wallets/{id}/topup", handler.TopUp(ledgerSvc, idemStore))
	mux.HandleFunc("POST /wallets/{id}/transfer", handler.Transfer(ledgerSvc, idemStore))
	mux.HandleFunc("GET /wallets/{id}/balance", handler.GetWalletBalance(ledgerSvc))
	mux.HandleFunc("GET /wallets/{id}/history", handler.GetWalletHistory(ledgerSvc))
	mux.HandleFunc("GET /integrity", handler.GetIntegrity(ledgerSvc))

	// Serves the static demo frontend (see /frontend at the repo root)
	// at every path not already claimed by a route above -- see the
	// root README's frontend section for why wallet-service is what
	// serves it. http.FileServer reads from disk on every request, so
	// editing frontend/*.html|css|js takes effect on refresh with no
	// rebuild or restart. This is intentionally the least-specific
	// route: Go's ServeMux matches the API routes above first for the
	// exact paths they claim, falling through to this for everything
	// else ("/", "/app.js", "/styles.css", ...).
	mux.Handle("/", noBrowserCache(http.FileServer(http.Dir(cfg.FrontendDir))))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("wallet-service listening on :%s (ledger-service at %s)", cfg.Port, cfg.LedgerServiceURL)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
