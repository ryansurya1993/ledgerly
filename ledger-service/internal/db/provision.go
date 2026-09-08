package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ErrAppRoleNotYetCreated is returned by ProvisionAppRolePassword when
// Config.AppUser's role doesn't exist in Postgres yet. In practice
// this means RunMigrations hasn't been run against this database --
// migration 000002_restrict_app_role is what creates the role in the
// first place (see its comments) -- so setting its password doesn't
// make sense yet. Prefer InitializeDatabase, which always runs the two
// in the correct order and so can never hit this; this exists so a
// caller that gets that order wrong anyway gets a specific, actionable
// error instead of a raw "role does not exist" failure from Postgres.
var ErrAppRoleNotYetCreated = errors.New("app role does not exist yet -- migrations must run before provisioning its password")

// ProvisionAppRolePassword sets ledger_app's password to
// Config.AppPassword, authenticated as the same admin/owner role
// RunMigrations uses. It must run after RunMigrations, since migration
// 000002_restrict_app_role is what creates the role in the first
// place -- with no password, deliberately (see that migration's
// comments): a .sql file is version-controlled and readable by anyone
// with repo access, so a real credential can never live in one. Calling
// this before RunMigrations returns ErrAppRoleNotYetCreated rather than
// attempting (and failing) the ALTER ROLE anyway.
//
// Doing this here, from the environment variable every environment
// already has to set (LEDGER_APP_DB_PASSWORD), removes what would
// otherwise be a manual "ALTER ROLE ledger_app WITH PASSWORD ..." step
// an operator has to remember to run by hand in every environment --
// local dev, CI, and production alike -- before the service can start.
// It doesn't weaken the "no credential in a committed file" property:
// the value still only ever comes from the environment, never from a
// .sql file. ALTER ROLE ... WITH PASSWORD is idempotent, so running it
// on every startup (not just the very first) is safe and cheap.
func ProvisionAppRolePassword(ctx context.Context, cfg Config) error {
	conn, err := pgx.Connect(ctx, cfg.dsn("postgres", cfg.MigrateUser, cfg.MigratePassword))
	if err != nil {
		return fmt.Errorf("connect as admin role: %w", err)
	}
	defer conn.Close(context.Background())

	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", cfg.AppUser).Scan(&exists); err != nil {
		return fmt.Errorf("check whether %s role exists: %w", cfg.AppUser, err)
	}
	if !exists {
		return fmt.Errorf("%s: %w", cfg.AppUser, ErrAppRoleNotYetCreated)
	}

	// ALTER ROLE's PASSWORD clause is parsed as a literal, not an
	// expression -- Postgres rejects a $1 placeholder there with a
	// syntax error, so this can't use a normal parameterized query.
	// Escaping is done by hand instead: double every single quote (the
	// standard SQL escape, and the only special character once
	// standard_conforming_strings is on, which it is by default), then
	// wrap the whole thing in quotes ourselves.
	escapedPassword := strings.ReplaceAll(cfg.AppPassword, "'", "''")
	stmt := fmt.Sprintf("ALTER ROLE %s WITH PASSWORD '%s'", pgx.Identifier{cfg.AppUser}.Sanitize(), escapedPassword)
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("set %s password: %w", cfg.AppUser, err)
	}

	return nil
}
