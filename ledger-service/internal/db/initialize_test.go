package db

import (
	"context"
	"testing"
	"time"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/dbtest"
)

// TestInitializeDatabase_MigratesThenProvisionsPassword confirms the
// composed, ordering-safe entrypoint main.go actually calls does both
// steps correctly: after it returns, the app role exists (migrations
// ran) and its password was set to Config.AppPassword (connecting as
// that role with that password succeeds).
func TestInitializeDatabase_MigratesThenProvisionsPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pg, stop, err := dbtest.StartPostgres(ctx, "ledgerly_initialize_test")
	if err != nil {
		t.Skipf("no test Postgres available (Docker missing or failed to start): %v", err)
	}
	defer stop()

	cfg := Config{
		Host:            pg.Host,
		Port:            pg.Port,
		Name:            pg.DBName,
		SSLMode:         "disable",
		AppUser:         "ledger_app",
		AppPassword:     "initialize_test_password",
		MigrateUser:     pg.AdminUser,
		MigratePassword: pg.AdminPassword,
	}

	if err := InitializeDatabase(ctx, cfg); err != nil {
		t.Fatalf("InitializeDatabase() error = %v", err)
	}

	pool, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("Connect() as app role after InitializeDatabase() error = %v", err)
	}
	pool.Close()
}
