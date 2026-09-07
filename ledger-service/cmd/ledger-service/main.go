package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/db"
	"github.com/ryansurya1993/ledgerly/ledger-service/internal/handler"
)

func main() {
	cfg, err := db.LoadConfig()
	if err != nil {
		log.Fatalf("load db config: %v", err)
	}

	startupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Migrations run first, over a connection authenticated as the
	// admin/owner role (Config.MigrateURL) -- never the role the
	// service serves requests with.
	if err := db.RunMigrations(cfg); err != nil {
		log.Fatalf("run migrations: %v", err)
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

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health(pool))

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
