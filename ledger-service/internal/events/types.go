package events

import "time"

// ExchangeName is the durable topic exchange ledger-service publishes
// to and notification-service consumes from. Declared (idempotently)
// by both sides, since either might start first -- see Publisher's and
// notification-service's connect logic.
//
// notification-service is a separate Go module with its own copy of
// this constant (and of TransactionPosted below) rather than importing
// this package -- the same reason wallet-service's internal/ledgerclient
// defines its own copy of ledger-service's response shapes instead of
// importing them (see that package's doc comment): independent
// modules, independent deploys, no shared-code coupling across a
// service boundary. If you change either of these, change them in both
// places.
const ExchangeName = "ledger.events"

// RoutingKeyTransactionPosted is the routing key TransactionPosted
// events are published under. A topic exchange (rather than publishing
// straight to a queue) costs nothing extra to operate here and leaves
// room for a future event type (e.g. "account.created") to be added
// under its own routing key without touching this one's consumers.
const RoutingKeyTransactionPosted = "transaction.posted"

// TransactionPostedEvent is the wire shape published for every
// genuinely new (non-replayed) posted transaction. Field types are
// wire-friendly (strings, not uuid.UUID) since this crosses a JSON
// boundary to a separate service/module -- see ExchangeName's doc
// comment.
type TransactionPostedEvent struct {
	TransactionID   string     `json:"transaction_id"`
	TransactionType string     `json:"transaction_type"`
	Description     string     `json:"description"`
	Legs            []EventLeg `json:"legs"`
	CreatedAt       time.Time  `json:"created_at"`
}

// EventLeg mirrors one leg of the posted transaction.
type EventLeg struct {
	AccountID string `json:"account_id"`
	Direction string `json:"direction"`
	Amount    int64  `json:"amount"`
}
