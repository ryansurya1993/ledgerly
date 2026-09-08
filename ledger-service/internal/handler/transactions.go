package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/ledger"
)

type legRequest struct {
	AccountID uuid.UUID `json:"account_id"`
	Direction string    `json:"direction"`
	Amount    int64     `json:"amount"`
}

type postTransactionRequest struct {
	IdempotencyKey  string       `json:"idempotency_key"`
	TransactionType string       `json:"transaction_type"`
	Description     string       `json:"description"`
	Legs            []legRequest `json:"legs"`
}

type transactionResponse struct {
	ID              uuid.UUID `json:"id"`
	IdempotencyKey  string    `json:"idempotency_key"`
	TransactionType string    `json:"transaction_type"`
	Description     string    `json:"description,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type entryResponse struct {
	ID            uuid.UUID `json:"id"`
	TransactionID uuid.UUID `json:"transaction_id"`
	AccountID     uuid.UUID `json:"account_id"`
	Direction     string    `json:"direction"`
	Amount        int64     `json:"amount"`
	CreatedAt     time.Time `json:"created_at"`
}

type postTransactionResponse struct {
	Transaction transactionResponse `json:"transaction"`
	Entries     []entryResponse     `json:"entries"`

	// Replayed is true when idempotency_key had already been processed
	// by an earlier call -- see internal/ledger.PostTransactionResult.
	// Transaction/Entries describe the *original* posting in that case,
	// not a new one.
	Replayed bool `json:"replayed"`
}

// PostTransaction handles POST /transactions.
//
//	{
//	  "idempotency_key": "…",
//	  "transaction_type": "transfer",
//	  "description": "optional",
//	  "legs": [
//	    {"account_id": "…", "direction": "debit", "amount": 500},
//	    {"account_id": "…", "direction": "credit", "amount": 500}
//	  ]
//	}
//
// Returns 201 for a newly-posted transaction, 200 if idempotency_key
// had already been processed (replayed: true either way tells the
// caller which happened). A malformed account_id inside a leg fails at
// JSON decode time (uuid.UUID implements encoding.TextUnmarshaler), so
// it's reported as a 400 alongside every other body-shape problem
// rather than needing separate handling.
func PostTransaction(svc LedgerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req postTransactionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed JSON body: "+err.Error())
			return
		}

		legs := make([]ledger.Leg, len(req.Legs))
		for i, lg := range req.Legs {
			legs[i] = ledger.Leg{
				AccountID: lg.AccountID,
				Direction: ledger.Direction(lg.Direction),
				Amount:    lg.Amount,
			}
		}

		result, err := svc.PostTransaction(r.Context(), ledger.PostTransactionParams{
			IdempotencyKey:  req.IdempotencyKey,
			TransactionType: ledger.TransactionType(req.TransactionType),
			Description:     req.Description,
			Legs:            legs,
		})
		if err != nil {
			writeLedgerError(w, err)
			return
		}

		entries := make([]entryResponse, len(result.Entries))
		for i, e := range result.Entries {
			entries[i] = entryResponse{
				ID:            e.ID,
				TransactionID: e.TransactionID,
				AccountID:     e.AccountID,
				Direction:     string(e.Direction),
				Amount:        e.Amount,
				CreatedAt:     e.CreatedAt,
			}
		}

		status := http.StatusCreated
		if result.Replayed {
			status = http.StatusOK
		}

		writeJSON(w, status, postTransactionResponse{
			Transaction: transactionResponse{
				ID:              result.Transaction.ID,
				IdempotencyKey:  result.Transaction.IdempotencyKey,
				TransactionType: string(result.Transaction.TransactionType),
				Description:     result.Transaction.Description,
				CreatedAt:       result.Transaction.CreatedAt,
			},
			Entries:  entries,
			Replayed: result.Replayed,
		})
	}
}
