package ledger

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ledgerdb "github.com/ryansurya1993/ledgerly/ledger-service/internal/db"
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

// startTestPostgres brings up a disposable Postgres container, applies
// every migration against it exactly as cmd/ledger-service/main.go
// does, provisions ledger_app's password (production sets this
// out-of-band per the README; tests do the equivalent here), and
// returns a runtime pool connected as ledger_app -- the same role and
// connection path the real service uses.
func startTestPostgres(ctx context.Context) (*pgxpool.Pool, func(), error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, nil, fmt.Errorf("docker not found in PATH: %w", err)
	}

	runCmd := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"-e", "POSTGRES_PASSWORD=postgres",
		"-e", "POSTGRES_DB=ledgerly_test",
		"-p", "127.0.0.1::5432",
		"postgres:16-alpine",
	)
	out, err := runCmd.CombinedOutput()
	if err != nil {
		return nil, nil, fmt.Errorf("docker run postgres: %w: %s", err, out)
	}
	containerID := strings.TrimSpace(string(out))

	stopContainer := func() {
		_ = exec.Command("docker", "stop", "-t", "3", containerID).Run()
	}

	port, err := containerHostPort(containerID)
	if err != nil {
		stopContainer()
		return nil, nil, err
	}

	adminDSN := (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword("postgres", "postgres"),
		Host:     net.JoinHostPort("127.0.0.1", port),
		Path:     "/ledgerly_test",
		RawQuery: "sslmode=disable",
	}).String()

	if err := waitForPostgres(ctx, adminDSN); err != nil {
		stopContainer()
		return nil, nil, err
	}

	cfg := ledgerdb.Config{
		Host:            "127.0.0.1",
		Port:            port,
		Name:            "ledgerly_test",
		SSLMode:         "disable",
		AppUser:         "ledger_app",
		AppPassword:     testAppPassword,
		MigrateUser:     "postgres",
		MigratePassword: "postgres",
	}

	if err := ledgerdb.RunMigrations(cfg); err != nil {
		stopContainer()
		return nil, nil, fmt.Errorf("run migrations: %w", err)
	}

	if err := setLedgerAppPassword(ctx, adminDSN, testAppPassword); err != nil {
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

// containerHostPort asks Docker which host port it mapped the
// container's 5432 to (we bind to port 0 / an empty host port above so
// concurrent test runs, or a developer's own local Postgres on 5432,
// can never collide with this container).
func containerHostPort(containerID string) (string, error) {
	out, err := exec.Command("docker", "port", containerID, "5432/tcp").Output()
	if err != nil {
		return "", fmt.Errorf("docker port: %w", err)
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	_, port, err := net.SplitHostPort(line)
	if err != nil {
		return "", fmt.Errorf("parse docker port output %q: %w", line, err)
	}
	return port, nil
}

// waitForPostgres retries connecting until Postgres inside the
// just-started container is actually accepting connections, or ctx runs
// out. A freshly `docker run` container needs a moment before its
// Postgres is ready; without this, the very first migration attempt
// would race the container's own startup.
func waitForPostgres(ctx context.Context, dsn string) error {
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("postgres did not become ready: %w (last error: %v)", ctx.Err(), lastErr)
		default:
		}

		connCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		conn, err := pgx.Connect(connCtx, dsn)
		cancel()
		if err == nil {
			conn.Close(context.Background())
			return nil
		}
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
}

// setLedgerAppPassword performs the one-time production step described
// in ledger-service/README.md's "Setting ledger_app's password" section
// -- migration 000002 deliberately creates the role with no password so
// a real credential never lives in version control, so every
// environment (including this ephemeral test one) has to set it
// out-of-band, exactly once, after migrations run.
func setLedgerAppPassword(ctx context.Context, adminDSN, password string) error {
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("connect as admin to set ledger_app password: %w", err)
	}
	defer conn.Close(context.Background())

	_, err = conn.Exec(ctx, fmt.Sprintf(`ALTER ROLE ledger_app WITH PASSWORD '%s'`, password))
	if err != nil {
		return fmt.Errorf("set ledger_app password: %w", err)
	}
	return nil
}
