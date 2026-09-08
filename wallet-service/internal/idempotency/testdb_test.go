package idempotency

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// testRDB is the shared Redis client every test in this package uses --
// a real, disposable Redis container, not a fake, for the same reason
// ledger-service's internal/ledger tests run against a real Postgres
// (see that package's testdb_test.go and CLAUDE.md's "verify infra
// against real deps" habit): what's under test here (redis.Nil
// handling, TTL expiry, actual serialization round trips) is exactly
// the behavior a fake would have to reimplement its own assumptions
// about, defeating the point of the test.
//
// If Docker isn't available, testRDB stays nil and every test skips via
// requireTestRedis -- see startTestRedis.
var testRDB *redis.Client

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rdb, cleanup, err := startTestRedis(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "idempotency: skipping integration tests, could not start test Redis: %v\n", err)
		os.Exit(m.Run())
	}
	testRDB = rdb

	code := m.Run()
	cleanup()
	os.Exit(code)
}

func requireTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	if testRDB == nil {
		t.Skip("no test Redis available (Docker missing or failed to start); see TestMain")
	}
	return testRDB
}

// startTestRedis brings up a disposable Redis container on a random
// host port and returns a connected client.
func startTestRedis(ctx context.Context) (*redis.Client, func(), error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, nil, fmt.Errorf("docker not found in PATH: %w", err)
	}

	runCmd := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"-p", "127.0.0.1::6379",
		"redis:7-alpine",
	)
	out, err := runCmd.CombinedOutput()
	if err != nil {
		return nil, nil, fmt.Errorf("docker run redis: %w: %s", err, out)
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

	addr := net.JoinHostPort("127.0.0.1", port)
	rdb := redis.NewClient(&redis.Options{Addr: addr})

	if err := waitForRedis(ctx, rdb); err != nil {
		rdb.Close()
		stopContainer()
		return nil, nil, err
	}

	cleanup := func() {
		rdb.Close()
		stopContainer()
	}
	return rdb, cleanup, nil
}

// containerHostPort asks Docker which host port it mapped the
// container's 6379 to. Immediately after `docker run -d` returns, the
// port mapping is occasionally not yet queryable, so this retries
// briefly rather than failing on the first attempt.
func containerHostPort(containerID string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		out, err := exec.Command("docker", "port", containerID, "6379/tcp").Output()
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

func waitForRedis(ctx context.Context, rdb *redis.Client) error {
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("redis did not become ready: %w (last error: %v)", ctx.Err(), lastErr)
		default:
		}

		pingCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		err := rdb.Ping(pingCtx).Err()
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
}
