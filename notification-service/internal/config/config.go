// Package config reads notification-service's runtime configuration
// from the environment.
package config

import "os"

// Config holds everything notification-service needs to start: where
// to listen, and how to reach RabbitMQ.
type Config struct {
	Port string

	// RabbitMQURL is a full AMQP URI, e.g. "amqp://user:pass@host:5672/".
	// Like ledger-service's own internal/events.Config.URL, this has a
	// same-machine-default fallback and is never required to be set --
	// an unreachable RabbitMQ doesn't stop this service from starting,
	// only from having anything to stream yet. See internal/events'
	// package doc for how it degrades and recovers.
	RabbitMQURL string
}

// Load reads Config from the environment.
func Load() Config {
	return Config{
		Port:        getenv("PORT", "8082"),
		RabbitMQURL: getenv("NOTIFICATION_RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
