// Package dbtest starts disposable, real Postgres containers for
// tests that need to exercise actual Postgres behavior (row locking,
// constraint enforcement, real role/privilege grants) rather than a
// mock. internal/ledger's and internal/db's tests both need this, so
// it lives here once rather than each package hand-rolling its own
// docker-run plumbing -- see CLAUDE.md's code-reuse rule.
package dbtest

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Postgres describes a running, disposable Postgres container: enough
// to build any service's own Config against it. It always starts with
// a single admin/owner superuser role (AdminUser/AdminPassword) and no
// application role -- callers that need one (e.g. ledger-service's
// ledger_app) provision it themselves via their own migrations, the
// same way production does.
type Postgres struct {
	Host          string
	Port          string
	DBName        string
	AdminUser     string
	AdminPassword string
}

// AdminDSN returns a plain "postgres://" connection string for the
// admin/owner role -- the shape needed to administer the database
// directly, before any service-specific role exists.
func (p *Postgres) AdminDSN() string {
	return (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(p.AdminUser, p.AdminPassword),
		Host:     net.JoinHostPort(p.Host, p.Port),
		Path:     "/" + p.DBName,
		RawQuery: "sslmode=disable",
	}).String()
}

// StartPostgres brings up a disposable postgres:16-alpine container
// (via `docker run -d --rm`, bound to an OS-assigned host port so
// concurrent test runs or a developer's own local Postgres never
// collide with it), waits for it to accept connections, and returns
// it along with a stop function that removes the container.
//
// If Docker isn't available or the container can't be started, this
// returns an error rather than failing hard -- callers should treat
// that as "skip this test", not a hard failure, so `go test ./...`
// still runs everywhere but is most meaningful where Docker is
// present. See internal/ledger's and internal/db's own
// requireTestDB-style skip helpers.
func StartPostgres(ctx context.Context, dbName string) (*Postgres, func(), error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, nil, fmt.Errorf("docker not found in PATH: %w", err)
	}

	const adminUser = "postgres"
	const adminPassword = "postgres"

	runCmd := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"-e", "POSTGRES_PASSWORD="+adminPassword,
		"-e", "POSTGRES_DB="+dbName,
		"-p", "127.0.0.1::5432",
		"postgres:16-alpine",
	)
	out, err := runCmd.CombinedOutput()
	if err != nil {
		return nil, nil, fmt.Errorf("docker run postgres: %w: %s", err, out)
	}
	containerID := strings.TrimSpace(string(out))

	stop := func() {
		_ = exec.Command("docker", "stop", "-t", "3", containerID).Run()
	}

	port, err := containerHostPort(containerID)
	if err != nil {
		stop()
		return nil, nil, err
	}

	pg := &Postgres{
		Host:          "127.0.0.1",
		Port:          port,
		DBName:        dbName,
		AdminUser:     adminUser,
		AdminPassword: adminPassword,
	}

	if err := waitForPostgres(ctx, pg.AdminDSN()); err != nil {
		stop()
		return nil, nil, err
	}

	return pg, stop, nil
}

// containerHostPort asks Docker which host port it mapped the
// container's 5432 to. Immediately after `docker run -d` returns, the
// port mapping is occasionally not yet queryable, so this retries
// briefly rather than failing on the first attempt.
func containerHostPort(containerID string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		out, err := exec.Command("docker", "port", containerID, "5432/tcp").Output()
		if err == nil {
			line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
			_, port, err := net.SplitHostPort(line)
			if err != nil {
				return "", fmt.Errorf("parse docker port output %q: %w", line, err)
			}
			return port, nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return "", fmt.Errorf("docker port: %w", lastErr)
}

// waitForPostgres retries connecting until Postgres inside the
// just-started container is actually accepting connections, or ctx
// runs out. A freshly `docker run` container needs a moment before
// its Postgres is ready; without this, the first real use of it would
// race the container's own startup.
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
