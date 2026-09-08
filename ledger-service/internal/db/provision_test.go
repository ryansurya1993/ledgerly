package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/dbtest"
)

// TestProvisionAppRolePassword_BeforeMigrationsFails is the guardrail
// this file exists to prove: calling ProvisionAppRolePassword against
// a database that hasn't been migrated yet (so the app role doesn't
// exist) must fail with ErrAppRoleNotYetCreated, a specific and
// actionable error -- not a raw "role does not exist" failure surfaced
// straight from Postgres, and never a silent success.
func TestProvisionAppRolePassword_BeforeMigrationsFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pg, stop, err := dbtest.StartPostgres(ctx, "ledgerly_provision_test")
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
		AppPassword:     "whatever",
		MigrateUser:     pg.AdminUser,
		MigratePassword: pg.AdminPassword,
	}

	// Deliberately never call RunMigrations -- ledger_app doesn't exist
	// on this fresh database yet, since migration 000002 is what
	// creates it.
	err = ProvisionAppRolePassword(ctx, cfg)
	if !errors.Is(err, ErrAppRoleNotYetCreated) {
		t.Fatalf("ProvisionAppRolePassword() before migrations: error = %v, want errors.Is(..., ErrAppRoleNotYetCreated)", err)
	}
}
