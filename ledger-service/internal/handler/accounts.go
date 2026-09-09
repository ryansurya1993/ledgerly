package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ryansurya1993/ledgerly/ledger-service/internal/ledger"
)

type createAccountRequest struct {
	Name        string `json:"name"`
	AccountType string `json:"account_type"`
	// Currency is optional; internal/ledger defaults it to "USD".
	Currency string `json:"currency"`
}

type accountResponse struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	AccountType string    `json:"account_type"`
	Currency    string    `json:"currency"`
	Balance     int64     `json:"balance"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func newAccountResponse(a ledger.Account) accountResponse {
	return accountResponse{
		ID:          a.ID,
		Name:        a.Name,
		AccountType: string(a.AccountType),
		Currency:    a.Currency,
		Balance:     a.Balance,
		Version:     a.Version,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
	}
}

// CreateAccount handles POST /accounts.
//
//	{"name": "Alice's Wallet", "account_type": "wallet", "currency": "USD"}
//
// currency may be omitted (defaults to USD). Validation of account_type
// happens in internal/ledger (ErrInvalidParams -> 400), not here, so
// there's exactly one place that knows what a valid account type is.
func CreateAccount(svc LedgerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed JSON body: "+err.Error())
			return
		}

		account, err := svc.CreateAccount(r.Context(), ledger.CreateAccountParams{
			Name:        req.Name,
			AccountType: ledger.AccountType(req.AccountType),
			Currency:    req.Currency,
		})
		if err != nil {
			writeLedgerError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, newAccountResponse(account))
	}
}

// ListAccounts handles GET /accounts, returning every wallet account --
// see Ledger.ListWalletAccounts for why system accounts (the external
// funding account) are excluded rather than offered as a filter here.
// No caller today needs an unfiltered or system-only listing (GET
// /integrity already serves "every account, including system ones," for
// the reconciliation use case), so this endpoint doesn't grow a query
// parameter for a distinction nothing yet uses.
func ListAccounts(svc LedgerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accounts, err := svc.ListWalletAccounts(r.Context())
		if err != nil {
			writeLedgerError(w, err)
			return
		}

		resp := make([]accountResponse, len(accounts))
		for i, a := range accounts {
			resp[i] = newAccountResponse(a)
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// parseAccountID extracts and parses the {id} path value shared by
// every /accounts/{id}/... route. On a malformed value it writes a 400
// response itself and returns ok=false, so callers can just
// `if !ok { return }`.
func parseAccountID(w http.ResponseWriter, r *http.Request) (id uuid.UUID, ok bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid account id: "+err.Error())
		return uuid.UUID{}, false
	}
	return id, true
}

type balanceResponse struct {
	AccountID uuid.UUID `json:"account_id"`
	Balance   int64     `json:"balance"`
}

// GetBalance handles GET /accounts/{id}/balance.
func GetBalance(svc LedgerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseAccountID(w, r)
		if !ok {
			return
		}

		balance, err := svc.GetBalance(r.Context(), id)
		if err != nil {
			writeLedgerError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, balanceResponse{AccountID: id, Balance: balance})
	}
}

type historyEntryResponse struct {
	ID              uuid.UUID `json:"id"`
	TransactionID   uuid.UUID `json:"transaction_id"`
	TransactionType string    `json:"transaction_type"`
	Description     string    `json:"description,omitempty"`
	Direction       string    `json:"direction"`
	Amount          int64     `json:"amount"`
	CreatedAt       time.Time `json:"created_at"`
}

type historyResponse struct {
	AccountID uuid.UUID              `json:"account_id"`
	Entries   []historyEntryResponse `json:"entries"`
}

// GetHistory handles GET /accounts/{id}/history.
//
// An account with no entries yet and a nonexistent account both start
// out looking the same (zero rows from the ledger_entries query), so
// they're disambiguated only in that case: the common case (an account
// with history) resolves in the one query below, and AccountExists is
// called only when that query comes back empty, to tell "empty" (200)
// apart from "doesn't exist" (404).
func GetHistory(svc LedgerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseAccountID(w, r)
		if !ok {
			return
		}

		history, err := svc.GetHistory(r.Context(), id)
		if err != nil {
			writeLedgerError(w, err)
			return
		}

		if len(history) == 0 {
			exists, err := svc.AccountExists(r.Context(), id)
			if err != nil {
				writeLedgerError(w, err)
				return
			}
			if !exists {
				writeLedgerError(w, fmt.Errorf("%w: %s", ledger.ErrAccountNotFound, id))
				return
			}
		}

		entries := make([]historyEntryResponse, len(history))
		for i, h := range history {
			entries[i] = historyEntryResponse{
				ID:              h.ID,
				TransactionID:   h.TransactionID,
				TransactionType: string(h.TransactionType),
				Description:     h.Description,
				Direction:       string(h.Direction),
				Amount:          h.Amount,
				CreatedAt:       h.CreatedAt,
			}
		}

		writeJSON(w, http.StatusOK, historyResponse{AccountID: id, Entries: entries})
	}
}

type integrityResponse struct {
	AccountID       uuid.UUID `json:"account_id"`
	CachedBalance   int64     `json:"cached_balance"`
	ComputedBalance int64     `json:"computed_balance"`
	Drifted         bool      `json:"drifted"`
}

func newIntegrityResponse(r ledger.IntegrityResult) integrityResponse {
	return integrityResponse{
		AccountID:       r.AccountID,
		CachedBalance:   r.CachedBalance,
		ComputedBalance: r.ComputedBalance,
		Drifted:         r.Drifted(),
	}
}

// GetAccountIntegrity handles GET /accounts/{id}/integrity.
func GetAccountIntegrity(svc LedgerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseAccountID(w, r)
		if !ok {
			return
		}

		result, err := svc.CheckAccountIntegrity(r.Context(), id)
		if err != nil {
			writeLedgerError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, newIntegrityResponse(result))
	}
}
