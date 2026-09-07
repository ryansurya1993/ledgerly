package db

import (
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Embedding the .sql files into the binary (rather than reading them
// from disk via a "file://" path) means the compiled service carries
// its own migrations wherever it runs -- no relative path or working
// directory assumption to get wrong inside a container.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// RunMigrations applies any pending migrations in migrations/, in
// order, using the privileged admin/owner role (Config.MigrateURL).
// That role's credentials are used nowhere else in the service -- the
// *migrate.Migrate instance built here is closed and discarded before
// this function returns, so there is no path by which the admin
// connection could accidentally get reused to serve a request. Runtime
// queries go through Connect/AppDSN instead, which this function never
// touches.
func RunMigrations(cfg Config) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, cfg.MigrateURL())
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}

	return nil
}
