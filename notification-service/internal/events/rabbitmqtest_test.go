package events

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// testRabbitMQ is a disposable RabbitMQ container for this package's
// tests, following the same "start it with `docker run`, wait for it
// to accept connections, skip gracefully if Docker isn't available"
// approach as ledger-service's internal/dbtest -- reimplemented here,
// not imported, since notification-service is a separate Go module
// (see internal/events/types.go's doc comment on why this project
// duplicates rather than shares code across service boundaries).
//
// Unlike dbtest.StartPostgres, this doesn't use --rm: stop() removes
// the container explicitly, and TestConsumer_ReconnectsAfterBrokerRestart
// needs to remove and recreate it mid-test (see restart) to simulate
// an outage.
type testRabbitMQ struct {
	containerID string
	port        int
	url         string
}

// startRabbitMQ brings up a disposable rabbitmq:3-alpine container on
// a free host port, waits for it to accept AMQP connections, and
// returns it along with a stop function that removes the container.
func startRabbitMQ(ctx context.Context) (*testRabbitMQ, func(), error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, nil, fmt.Errorf("docker not found in PATH: %w", err)
	}

	port, err := freePort()
	if err != nil {
		return nil, nil, fmt.Errorf("find a free port: %w", err)
	}

	containerID, err := runRabbitMQContainer(ctx, port)
	if err != nil {
		return nil, nil, err
	}

	r := &testRabbitMQ{
		containerID: containerID,
		port:        port,
		url:         fmt.Sprintf("amqp://guest:guest@127.0.0.1:%d/", port),
	}
	stop := func() { removeContainer(r.containerID) }

	if err := waitForRabbitMQ(ctx, r.url); err != nil {
		stop()
		return nil, nil, err
	}
	return r, stop, nil
}

// runRabbitMQContainer starts a fresh rabbitmq:3-alpine container
// publishing its AMQP port on 127.0.0.1:hostPort, and returns its
// container ID.
func runRabbitMQContainer(ctx context.Context, hostPort int) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "run", "-d",
		"-p", fmt.Sprintf("127.0.0.1:%d:5672", hostPort),
		"rabbitmq:3-alpine",
	).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker run rabbitmq: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// removeContainer force-removes a container, retrying once: a
// transient docker-daemon hiccup (or the container's own client
// connections still being torn down) shouldn't leak a container a
// plain `docker rm -f` a moment later would have removed fine.
func removeContainer(containerID string) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := exec.Command("docker", "rm", "-f", containerID).Run(); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// kill removes the container immediately, simulating a broker outage
// starting right now. Split out from revive (below) rather than one
// combined "restart" so a test can assert on the disconnected window
// in between -- reviving a fresh container can itself take several
// seconds (see revive's doc comment), long enough that a Consumer's
// own reconnect could otherwise land *during* that call and be missed
// entirely by a caller that only checks for "disconnected" after a
// combined restart step returns.
func (r *testRabbitMQ) kill() {
	removeContainer(r.containerID)
}

// revive starts a brand new container on the same host port (r.port)
// and waits for it to accept AMQP connections, simulating the broker
// coming back at the same address -- rather than `docker stop` +
// `docker start` on the original container. The obvious approach --
// stop/start the same container -- turns out to hit a real bug in the
// official rabbitmq image on at least some Docker setups (observed
// here under WSL2): the second boot can fail with "Error when reading
// /var/lib/rabbitmq/.erlang.cookie: eacces", because the anonymous
// data volume's ownership doesn't survive the restart cleanly. A fresh
// container gets a fresh volume with correct permissions from a clean
// entrypoint run, which sidesteps that bug entirely while still
// exercising exactly what the test cares about: the same address
// (127.0.0.1:port) going from unreachable back to reachable, which is
// indistinguishable from Consumer's point of view either way.
func (r *testRabbitMQ) revive(ctx context.Context) error {
	containerID, err := runRabbitMQContainer(ctx, r.port)
	if err != nil {
		return err
	}
	r.containerID = containerID

	return waitForRabbitMQ(ctx, r.url)
}

// freePort asks the OS for a free TCP port by briefly binding to
// port 0 and reading back what it was assigned, then releasing it
// immediately so `docker run` can bind it instead. Inherently
// racy in general (another process could grab the same port in the
// gap), but standard practice for tests and more than reliable enough
// here.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitForRabbitMQ retries dialing until the broker inside the
// container is actually accepting AMQP connections (RabbitMQ takes a
// few seconds longer to become ready than a bare TCP accept would
// suggest -- it accepts the socket before the AMQP protocol handshake
// it performs is actually ready to succeed), or ctx runs out.
func waitForRabbitMQ(ctx context.Context, url string) error {
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("rabbitmq did not become ready: %w (last error: %v)", ctx.Err(), lastErr)
		default:
		}

		conn, err := amqp.DialConfig(url, amqp.Config{Dial: amqp.DefaultDial(2 * time.Second)})
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(300 * time.Millisecond)
	}
}
