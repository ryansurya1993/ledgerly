package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/db"
	"github.com/ryansurya1993/ledgerly/ledger-service/internal/events"
	"github.com/ryansurya1993/ledgerly/ledger-service/internal/handler"
	"github.com/ryansurya1993/ledgerly/ledger-service/internal/ledger"
)

func main() {
	cfg, err := db.LoadConfig()
	if err != nil {
		log.Fatalf("load db config: %v", err)
	}

	startupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Migrates the schema and sets ledger_app's password, in that order
	// -- see db.InitializeDatabase's doc comment for why this is a
	// single call rather than two separate ones main.go could
	// accidentally reorder.
	if err := db.InitializeDatabase(startupCtx, cfg); err != nil {
		log.Fatalf("initialize database: %v", err)
	}

	// The pool the service actually queries through, authenticated as
	// the restricted ledger_app role (Config.AppDSN). The admin
	// connection used above is already closed by this point and is
	// never involved again.
	pool, err := db.Connect(startupCtx, cfg)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	// RabbitMQ is not a dependency the service needs to be reachable to
	// serve traffic correctly -- see internal/ledger/events.go's doc
	// comment -- so an unreachable broker here is logged, not fatal.
	publisher, err := events.NewPublisher(events.LoadConfig())
	if err != nil {
		log.Printf("events: initial RabbitMQ connection failed, will retry on first publish: %v", err)
	}
	defer publisher.Close()

	ledgerSvc := ledger.New(pool, publisher)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health(pool))
	mux.HandleFunc("POST /accounts", handler.CreateAccount(ledgerSvc))
	mux.HandleFunc("GET /accounts", handler.ListAccounts(ledgerSvc))
	mux.HandleFunc("GET /accounts/{id}/balance", handler.GetBalance(ledgerSvc))
	mux.HandleFunc("GET /accounts/{id}/history", handler.GetHistory(ledgerSvc))
	mux.HandleFunc("GET /accounts/{id}/integrity", handler.GetAccountIntegrity(ledgerSvc))
	mux.HandleFunc("POST /transactions", handler.PostTransaction(ledgerSvc))
	mux.HandleFunc("GET /integrity", handler.GetAllIntegrity(ledgerSvc))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("ledger-service listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
