package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/ryansurya1993/ledgerly/notification-service/internal/config"
	"github.com/ryansurya1993/ledgerly/notification-service/internal/events"
	"github.com/ryansurya1993/ledgerly/notification-service/internal/handler"
	"github.com/ryansurya1993/ledgerly/notification-service/internal/stream"
)

func main() {
	cfg := config.Load()

	hub := stream.NewHub()

	// The consumer re-encodes nothing itself -- it hands the hub a
	// ready-to-broadcast []byte, encoded once here rather than once per
	// connected client (see internal/handler.Stream's doc comment).
	consumer := events.NewConsumer(cfg.RabbitMQURL, func(evt events.TransactionPostedEvent) {
		payload, err := json.Marshal(evt)
		if err != nil {
			log.Printf("notification: failed to re-encode event %s for streaming: %v", evt.TransactionID, err)
			return
		}
		hub.Publish(payload)
	})

	// Runs for the life of the process, reconnecting with backoff on
	// its own -- see internal/events.Consumer's doc comment for why
	// this never crashes the process over RabbitMQ being unreachable.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go consumer.Run(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health(consumer))
	mux.HandleFunc("GET /events", handler.Stream(hub))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("notification-service listening on :%s", cfg.Port)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
