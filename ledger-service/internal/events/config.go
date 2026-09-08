// Package events publishes ledger-service's domain events to RabbitMQ
// for notification-service to consume -- currently just "a transaction
// was posted," for its live activity feed. See Publisher's doc comment
// for the delivery-guarantee tradeoff this makes, and
// internal/ledger/events.go for why that tradeoff is the right one for
// this project's scope.
package events

import "os"

// Config holds everything needed to connect to RabbitMQ.
type Config struct {
	// URL is a full AMQP URI, e.g. "amqp://user:pass@host:5672/". Unlike
	// internal/db.LoadConfig's Postgres credentials, this has a
	// same-machine-default fallback and is never required -- RabbitMQ is
	// not a dependency the service needs to be reachable to serve
	// traffic correctly (see Publisher's doc comment), so there's
	// nothing to fail startup over if it's misconfigured.
	URL string
}

// LoadConfig reads Config from the environment.
func LoadConfig() Config {
	return Config{
		URL: getenv("LEDGER_RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
