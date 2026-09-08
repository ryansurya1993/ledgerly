package db

import (
	"context"
	"fmt"
)

// InitializeDatabase brings a Postgres database up to the state the
// service expects to run against before it starts serving traffic:
// schema migrated, and the app role's password set to match
// Config.AppPassword. It always runs RunMigrations before
// ProvisionAppRolePassword, never the other way around -- migration
// 000002_restrict_app_role is what creates the app role in the first
// place, so provisioning its password only makes sense once migrations
// have applied (see ProvisionAppRolePassword's doc comment and
// ErrAppRoleNotYetCreated for what happens if that order is violated).
//
// cmd/ledger-service/main.go calls only this function, never
// RunMigrations or ProvisionAppRolePassword directly, so the ordering
// between them can't be gotten wrong by accident at the call site. The
// two steps stay separately exported (and separately tested) for
// finer-grained testing -- e.g. of ProvisionAppRolePassword's behavior
// when called before migrations have run.
func InitializeDatabase(ctx context.Context, cfg Config) error {
	if err := RunMigrations(cfg); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	if err := ProvisionAppRolePassword(ctx, cfg); err != nil {
		return fmt.Errorf("provision app role password: %w", err)
	}
	return nil
}
