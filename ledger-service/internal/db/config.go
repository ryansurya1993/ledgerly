package db

import (
	"fmt"
	"net"
	"net/url"
	"os"
)

// Config holds everything needed to build ledger-service's two distinct
// Postgres connections: the low-privilege ledger_app role the service
// queries as at runtime, and the admin/owner role migrations run as.
// These are kept as two separate credential pairs on purpose -- see
// ledger-service/README.md's "Database roles" section. AppDSN and
// MigrateURL below are the only two ways to turn this config into a
// connection string, and nothing in this package ever mixes
// AppUser/AppPassword with MigrateUser/MigratePassword into the same
// string.
//
// Host and port are also kept separate per role, not just credentials:
// in production and docker-compose, AppHost/AppPort point at PgBouncer
// (so many replicas' pools share a small number of real Postgres
// backend connections), while MigrateHost/MigratePort point at
// Postgres directly. Migrations must never go through PgBouncer in
// transaction-pooling mode -- see MigrateURL's doc comment for why.
type Config struct {
	AppHost string
	AppPort string

	MigrateHost string
	MigratePort string

	Name    string
	SSLMode string

	AppUser     string
	AppPassword string

	MigrateUser     string
	MigratePassword string
}

// LoadConfig reads connection details from the environment. Database
// name and SSL mode are shared by both roles; everything else can
// differ. Passwords have no default and are required -- failing fast
// here with a clear error beats a cryptic authentication failure
// surfacing later, once the service is already accepting traffic.
//
// MigrateHost/MigratePort default to AppHost/AppPort when unset, so a
// single-Postgres-no-PgBouncer setup (bare `go run`, or any environment
// that hasn't added PgBouncer) needs zero new configuration -- only an
// environment that actually puts PgBouncer in front of AppHost/AppPort
// needs to set MigrateHost/MigratePort explicitly to bypass it. See
// docker-compose.yml's ledger-service environment for exactly that.
func LoadConfig() (Config, error) {
	appHost := getenv("LEDGER_DB_HOST", "localhost")
	appPort := getenv("LEDGER_DB_PORT", "5432")

	cfg := Config{
		AppHost: appHost,
		AppPort: appPort,
		Name:    getenv("LEDGER_DB_NAME", "ledgerly"),
		SSLMode: getenv("LEDGER_DB_SSLMODE", "disable"),

		AppUser:     getenv("LEDGER_APP_DB_USER", "ledger_app"),
		AppPassword: os.Getenv("LEDGER_APP_DB_PASSWORD"),

		MigrateHost: getenv("LEDGER_MIGRATE_DB_HOST", appHost),
		MigratePort: getenv("LEDGER_MIGRATE_DB_PORT", appPort),

		MigrateUser:     getenv("LEDGER_MIGRATE_DB_USER", "postgres"),
		MigratePassword: os.Getenv("LEDGER_MIGRATE_DB_PASSWORD"),
	}

	if cfg.AppPassword == "" {
		return Config{}, fmt.Errorf("LEDGER_APP_DB_PASSWORD is required")
	}
	if cfg.MigratePassword == "" {
		return Config{}, fmt.Errorf("LEDGER_MIGRATE_DB_PASSWORD is required")
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// AppDSN builds the connection string for the service's own runtime
// queries: always the restricted ledger_app role, and (in any
// environment that sets one up) through PgBouncer rather than straight
// to Postgres. This is the only DSN pool.go is allowed to use.
func (c Config) AppDSN() string {
	return c.dsn("postgres", c.AppHost, c.AppPort, c.AppUser, c.AppPassword)
}

// MigrateURL builds the connection string used only to run migrations,
// authenticated as the privileged admin/owner role, always directly
// against Postgres -- never through PgBouncer. The "pgx5" scheme is
// what golang-migrate's pgx/v5 database driver expects, and is specific
// to that library -- it is not a general-purpose Postgres URL, which is
// why it's named differently from AppDSN. Nothing outside migrate.go
// (and internal/db/provision.go's admin connection, which needs the
// same direct-to-Postgres access for the same reason) should call this.
//
// Migrations must bypass PgBouncer specifically when it's configured
// for transaction pooling (the mode that actually saves connections,
// and the one this project uses -- see docker-compose.yml and
// ledger-service/README.md's PgBouncer section): golang-migrate
// serializes concurrent migration attempts from multiple
// simultaneously-starting replicas using a Postgres advisory lock,
// which is scoped to one session. Transaction pooling can hand a
// session's next transaction to a *different* backend connection than
// the one that took the lock, silently breaking that serialization --
// two replicas could then race to apply the same migration. Connecting
// straight to Postgres for migrations sidesteps this entirely.
func (c Config) MigrateURL() string {
	return c.dsn("pgx5", c.MigrateHost, c.MigratePort, c.MigrateUser, c.MigratePassword)
}

func (c Config) dsn(scheme, host, port, user, password string) string {
	u := url.URL{
		Scheme: scheme,
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + c.Name,
	}
	q := u.Query()
	q.Set("sslmode", c.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}
