package ledger

import (
	"time"

	"github.com/google/uuid"
)

// ExternalFundingAccountID is the fixed system account every top-up's
// debit leg lands on -- seeded by migration 000001_init_schema.up.sql.
// Money entering the ledger has to come from somewhere on the ledger,
// even though it's conceptually "external"; this account is that
// somewhere. wallet-service references this constant directly rather
// than looking the account up by name.
var ExternalFundingAccountID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// Direction is which side of a ledger entry a leg falls on. See the
// accounts table comment in migration 000001 for the accounting
// convention this package follows: crediting an account increases its
// cached balance, debiting it decreases it -- uniformly, for every
// account type. That convention is what makes a top-up "debit the
// external funding account, credit the wallet" come out correctly
// increasing the wallet and (correctly) driving the funding account
// negative.
type Direction string

const (
	Debit  Direction = "debit"
	Credit Direction = "credit"
)

// delta returns the signed effect this direction has on a cached
// account balance, per the convention above.
func (d Direction) delta(amount int64) int64 {
	if d == Debit {
		return -amount
	}
	return amount
}

// AccountType mirrors the accounts.account_type CHECK constraint.
type AccountType string

const (
	AccountTypeWallet AccountType = "wallet"
	AccountTypeSystem AccountType = "system"
)

// TransactionType mirrors the transactions.transaction_type CHECK
// constraint.
type TransactionType string

const (
	TransactionTypeTopUp    TransactionType = "top_up"
	TransactionTypeTransfer TransactionType = "transfer"
)

// Account is a row from the accounts table.
type Account struct {
	ID          uuid.UUID
	Name        string
	AccountType AccountType
	Currency    string
	Balance     int64 // minor units (cents); cached, see migration 000001
	Version     int64 // optimistic concurrency token
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Transaction is a row from the transactions table: one logical
// operation (a top-up, a transfer), identified for dedupe purposes by
// its idempotency key.
type Transaction struct {
	ID              uuid.UUID
	IdempotencyKey  string
	TransactionType TransactionType
	Description     string
	CreatedAt       time.Time
}

// Entry is a single debit or credit leg from ledger_entries.
type Entry struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	AccountID     uuid.UUID
	Direction     Direction
	Amount        int64
	CreatedAt     time.Time
}

// Leg is one side of a transaction the caller wants posted: "move
// $Amount across $AccountID in $Direction". PostTransaction takes a
// slice of these; a top-up has two (external funding debit, wallet
// credit), a transfer has two (source debit, destination credit), and
// nothing in this package assumes exactly two -- a future transaction
// type with more legs (e.g. a split payment) works without a code
// change, as long as debits still sum to credits.
type Leg struct {
	AccountID uuid.UUID
	Direction Direction
	Amount    int64
}

// HistoryEntry is one row of an account's transaction history: a
// ledger entry enriched with its parent transaction's type and
// description, since a raw Entry alone isn't enough to render a wallet
// history view.
type HistoryEntry struct {
	Entry
	TransactionType TransactionType
	Description     string
}
