package ledgerclient

import "time"

// Direction and transaction-type string values, matching
// ledger-service's internal/ledger.Direction /
// internal/ledger.TransactionType constants exactly (see that
// package's types.go). Kept as plain string constants here rather than
// a distinct Go type, since these cross the HTTP boundary as JSON
// strings either way.
const (
	DirectionDebit  = "debit"
	DirectionCredit = "credit"
)

const (
	TransactionTypeTopUp    = "top_up"
	TransactionTypeTransfer = "transfer"
)

// ExternalFundingAccountID is ledger-service's fixed system account
// every top-up's debit leg lands on -- seeded by its migration
// 000001_init_schema.up.sql and referenced as a constant by
// ledger-service's own internal/ledger package. wallet-service has no
// Go package to import that constant from (separate module -- see the
// package doc comment), so it's duplicated here deliberately, once,
// rather than left as a magic string at each call site.
const ExternalFundingAccountID = "00000000-0000-0000-0000-000000000001"

// CreateAccountRequest is the body of ledger-service's POST /accounts.
type CreateAccountRequest struct {
	Name        string `json:"name"`
	AccountType string `json:"account_type"`
	Currency    string `json:"currency,omitempty"`
}

// Account is ledger-service's account resource, returned by
// POST /accounts.
type Account struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	AccountType string    `json:"account_type"`
	Currency    string    `json:"currency"`
	Balance     int64     `json:"balance"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Leg is one debit or credit leg of a transaction to post.
type Leg struct {
	AccountID string `json:"account_id"`
	Direction string `json:"direction"`
	Amount    int64  `json:"amount"`
}

// PostTransactionRequest is the body of ledger-service's
// POST /transactions.
type PostTransactionRequest struct {
	IdempotencyKey  string `json:"idempotency_key"`
	TransactionType string `json:"transaction_type"`
	Description     string `json:"description,omitempty"`
	Legs            []Leg  `json:"legs"`
}

// Transaction is ledger-service's transaction resource.
type Transaction struct {
	ID              string    `json:"id"`
	IdempotencyKey  string    `json:"idempotency_key"`
	TransactionType string    `json:"transaction_type"`
	Description     string    `json:"description,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// Entry is one posted ledger entry (a debit or credit leg that actually
// landed).
type Entry struct {
	ID            string    `json:"id"`
	TransactionID string    `json:"transaction_id"`
	AccountID     string    `json:"account_id"`
	Direction     string    `json:"direction"`
	Amount        int64     `json:"amount"`
	CreatedAt     time.Time `json:"created_at"`
}

// PostTransactionResponse is ledger-service's response to
// POST /transactions.
type PostTransactionResponse struct {
	Transaction Transaction `json:"transaction"`
	Entries     []Entry     `json:"entries"`

	// Replayed is true if this idempotency_key had already been
	// processed -- see ledger-service's
	// internal/ledger.PostTransactionResult.Replayed. wallet-service
	// uses this to decide its own response status (200 vs 201) rather
	// than needing ledger-service's raw HTTP status code.
	Replayed bool `json:"replayed"`
}

// Balance is ledger-service's response to GET /accounts/{id}/balance.
type Balance struct {
	AccountID string `json:"account_id"`
	Balance   int64  `json:"balance"`
}

// HistoryEntry is one entry from ledger-service's
// GET /accounts/{id}/history.
type HistoryEntry struct {
	ID              string    `json:"id"`
	TransactionID   string    `json:"transaction_id"`
	TransactionType string    `json:"transaction_type"`
	Description     string    `json:"description,omitempty"`
	Direction       string    `json:"direction"`
	Amount          int64     `json:"amount"`
	CreatedAt       time.Time `json:"created_at"`
}

// History is ledger-service's response to GET /accounts/{id}/history.
type History struct {
	AccountID string         `json:"account_id"`
	Entries   []HistoryEntry `json:"entries"`
}

// IntegrityResult is one account's drift check, as returned by
// ledger-service's GET /integrity. AccountID deliberately isn't
// renamed to "wallet_id" the way Balance/History's fields are
// elsewhere in this package: results here can include ledger-internal
// accounts (e.g. the external funding account) that aren't wallets at
// all, so "account" is the accurate term.
type IntegrityResult struct {
	AccountID       string `json:"account_id"`
	CachedBalance   int64  `json:"cached_balance"`
	ComputedBalance int64  `json:"computed_balance"`
	Drifted         bool   `json:"drifted"`
}

// IntegrityResponse is ledger-service's response to GET /integrity.
type IntegrityResponse struct {
	Results []IntegrityResult `json:"results"`
	Drifted bool              `json:"drifted"`
}
