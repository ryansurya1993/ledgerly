package ledger

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	ledgerdb "github.com/ryansurya1993/ledgerly/ledger-service/internal/db"
	"github.com/ryansurya1993/ledgerly/ledger-service/internal/dbtest"
)

// testAppPassword is a throwaway credential used only inside an
// ephemeral, --rm'd container that no other process can reach. It is
// never a real secret, so unlike the production password (see
// ledger-service/README.md) it's fine to hardcode here.
const testAppPassword = "ledger_app_test_password"

// testPool is the shared *pgxpool.Pool every test in this package
// queries through, authenticated as the restricted ledger_app role --
// exactly the pool production code would build via db.Connect. It's set
// up once in TestMain against a real, disposable Postgres container
// rather than mocked, because the logic under test (post_transaction.go)
// depends on actual row-locking, actual CHECK constraint enforcement,
// and the actual ledger_app privilege grants from migration 000002 --
// no mock reproduces those faithfully, and a mock that tried would just
// be re-encoding assumptions about Postgres's behavior that this
// package's whole job is to get right.
//
// If Docker isn't available, testPool stays nil and every test skips
// via requireTestDB -- see [startTestPostgres] instead of hard-failing,
// so `go test ./...` still runs everywhere but is most meaningful where
// Docker is present.
var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, cleanup, err := startTestPostgres(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ledger: skipping integration tests, could not start test Postgres: %v\n", err)
		os.Exit(m.Run())
	}
	testPool = pool

	code := m.Run()
	cleanup()
	os.Exit(code)
}

// requireTestDB skips t if TestMain couldn't start a test database.
func requireTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testPool == nil {
		t.Skip("no test Postgres available (Docker missing or failed to start); see TestMain")
	}
	return testPool
}

// startTestPostgres brings up a disposable Postgres container (via
// dbtest.StartPostgres -- shared with internal/db's own tests) and
// runs db.InitializeDatabase against it, exactly as
// cmd/ledger-service/main.go does on every real startup, then returns
// a runtime pool connected as ledger_app -- the same role and
// connection path the real service uses.
func startTestPostgres(ctx context.Context) (*pgxpool.Pool, func(), error) {
	pg, stopContainer, err := dbtest.StartPostgres(ctx, "ledgerly_test")
	if err != nil {
		return nil, nil, err
	}

	cfg := ledgerdb.Config{
		// No PgBouncer in tests -- App and Migrate point at the same
		// container.
		AppHost:         pg.Host,
		AppPort:         pg.Port,
		MigrateHost:     pg.Host,
		MigratePort:     pg.Port,
		Name:            pg.DBName,
		SSLMode:         "disable",
		AppUser:         "ledger_app",
		AppPassword:     testAppPassword,
		MigrateUser:     pg.AdminUser,
		MigratePassword: pg.AdminPassword,
	}

	if err := ledgerdb.InitializeDatabase(ctx, cfg); err != nil {
		stopContainer()
		return nil, nil, err
	}

	pool, err := ledgerdb.Connect(ctx, cfg)
	if err != nil {
		stopContainer()
		return nil, nil, fmt.Errorf("connect as ledger_app: %w", err)
	}

	cleanup := func() {
		pool.Close()
		stopContainer()
	}
	return pool, cleanup, nil
}
