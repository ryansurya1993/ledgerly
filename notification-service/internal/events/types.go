package events

import "time"

// ExchangeName and RoutingKeyTransactionPosted must match
// ledger-service's internal/events package exactly -- notification-service
// is a separate Go module, so this is a deliberately duplicated
// constant, not a shared one. See ledger-service's
// internal/events/types.go for why (the same reasoning as
// wallet-service's internal/ledgerclient duplicating ledger-service's
// response shapes instead of importing them).
const (
	ExchangeName                = "ledger.events"
	RoutingKeyTransactionPosted = "transaction.posted"
)

// TransactionPostedEvent mirrors ledger-service's
// internal/events.TransactionPostedEvent -- the JSON shape published
// to ExchangeName under RoutingKeyTransactionPosted. Keep this in sync
// with that type by hand; nothing enforces it across the module
// boundary.
type TransactionPostedEvent struct {
	TransactionID   string     `json:"transaction_id"`
	TransactionType string     `json:"transaction_type"`
	Description     string     `json:"description"`
	Legs            []EventLeg `json:"legs"`
	CreatedAt       time.Time  `json:"created_at"`
}

// EventLeg mirrors ledger-service's internal/events.EventLeg.
type EventLeg struct {
	AccountID string `json:"account_id"`
	Direction string `json:"direction"`
	Amount    int64  `json:"amount"`
}
