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
type Config struct {
	Host    string
	Port    string
	Name    string
	SSLMode string

	AppUser     string
	AppPassword string

	MigrateUser     string
	MigratePassword string
}

// LoadConfig reads connection details from the environment. Host, port,
// database name, and SSL mode are shared by both roles; only the
// credentials differ. Passwords have no default and are required --
// failing fast here with a clear error beats a cryptic authentication
// failure surfacing later, once the service is already accepting
// traffic.
func LoadConfig() (Config, error) {
	cfg := Config{
		Host:    getenv("LEDGER_DB_HOST", "localhost"),
		Port:    getenv("LEDGER_DB_PORT", "5432"),
		Name:    getenv("LEDGER_DB_NAME", "ledgerly"),
		SSLMode: getenv("LEDGER_DB_SSLMODE", "disable"),

		AppUser:     getenv("LEDGER_APP_DB_USER", "ledger_app"),
		AppPassword: os.Getenv("LEDGER_APP_DB_PASSWORD"),

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
// queries: always the restricted ledger_app role. This is the only DSN
// pool.go is allowed to use.
func (c Config) AppDSN() string {
	return c.dsn("postgres", c.AppUser, c.AppPassword)
}

// MigrateURL builds the connection string used only to run migrations,
// authenticated as the privileged admin/owner role. The "pgx5" scheme
// is what golang-migrate's pgx/v5 database driver expects, and is
// specific to that library -- it is not a general-purpose Postgres URL,
// which is why it's named differently from AppDSN. Nothing outside
// migrate.go should call this.
func (c Config) MigrateURL() string {
	return c.dsn("pgx5", c.MigrateUser, c.MigratePassword)
}

func (c Config) dsn(scheme, user, password string) string {
	u := url.URL{
		Scheme: scheme,
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(c.Host, c.Port),
		Path:   "/" + c.Name,
	}
	q := u.Query()
	q.Set("sslmode", c.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}
