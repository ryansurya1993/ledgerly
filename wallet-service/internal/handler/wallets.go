package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ryansurya1993/ledgerly/wallet-service/internal/idempotency"
	"github.com/ryansurya1993/ledgerly/wallet-service/internal/ledgerclient"
)

type createWalletRequest struct {
	Name string `json:"name"`
	// Currency is optional; ledger-service defaults it to "USD".
	Currency string `json:"currency,omitempty"`
}

type walletResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Currency  string    `json:"currency"`
	Balance   int64     `json:"balance"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func newWalletResponse(a ledgerclient.Account) walletResponse {
	return walletResponse{
		ID:        a.ID,
		Name:      a.Name,
		Currency:  a.Currency,
		Balance:   a.Balance,
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}
}

// CreateWallet handles POST /wallets: {"name": "...", "currency": "USD"}.
// account_type is always "wallet" here -- ledger-service's "system"
// account type is an internal contra-account concept (like the external
// funding source) that a wallet-service visitor never creates directly.
// The response deliberately omits ledger-service's account_type and
// version fields: they're ledger implementation details, not part of a
// wallet-shaped API.
func CreateWallet(ledger LedgerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createWalletRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed JSON body: "+err.Error())
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}

		account, err := ledger.CreateAccount(r.Context(), ledgerclient.CreateAccountRequest{
			Name:        req.Name,
			AccountType: "wallet",
			Currency:    req.Currency,
		})
		if err != nil {
			writeLedgerClientError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, newWalletResponse(account))
	}
}

// ListWallets handles GET /wallets: a passthrough to ledger-service's
// GET /accounts, which already excludes system accounts like the
// external funding source (see its Ledger.ListWalletAccounts doc
// comment) -- so every account this returns is a real, selectable
// wallet. Same convention as CreateWallet's response: account_type and
// version are ledger-service implementation details, omitted here since
// they're not part of a wallet-shaped API.
func ListWallets(ledger LedgerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accounts, err := ledger.ListAccounts(r.Context())
		if err != nil {
			writeLedgerClientError(w, err)
			return
		}

		resp := make([]walletResponse, len(accounts))
		for i, a := range accounts {
			resp[i] = newWalletResponse(a)
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// parseWalletID extracts and parses the {id} path value shared by every
// /wallets/{id}/... route. On a malformed value it writes a 400
// response itself and returns ok=false, so callers can just
// `if !ok { return }`.
func parseWalletID(w http.ResponseWriter, r *http.Request) (id uuid.UUID, ok bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid wallet id: "+err.Error())
		return uuid.UUID{}, false
	}
	return id, true
}

type balanceResponse struct {
	WalletID string `json:"wallet_id"`
	Balance  int64  `json:"balance"`
}

// GetWalletBalance handles GET /wallets/{id}/balance.
func GetWalletBalance(ledger LedgerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseWalletID(w, r)
		if !ok {
			return
		}

		balance, err := ledger.GetBalance(r.Context(), id.String())
		if err != nil {
			writeLedgerClientError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, balanceResponse{WalletID: id.String(), Balance: balance.Balance})
	}
}

type historyEntryResponse struct {
	ID              string    `json:"id"`
	TransactionID   string    `json:"transaction_id"`
	TransactionType string    `json:"transaction_type"`
	Description     string    `json:"description,omitempty"`
	Direction       string    `json:"direction"`
	Amount          int64     `json:"amount"`
	CreatedAt       time.Time `json:"created_at"`
}

type historyResponse struct {
	WalletID string                 `json:"wallet_id"`
	Entries  []historyEntryResponse `json:"entries"`
}

// GetWalletHistory handles GET /wallets/{id}/history. Errors (including
// a 404 for a nonexistent wallet) come straight from ledger-service's
// own GetHistory endpoint, which already disambiguates "no history yet"
// from "doesn't exist" -- see ledger-service/README.md.
func GetWalletHistory(ledger LedgerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseWalletID(w, r)
		if !ok {
			return
		}

		history, err := ledger.GetHistory(r.Context(), id.String())
		if err != nil {
			writeLedgerClientError(w, err)
			return
		}

		entries := make([]historyEntryResponse, len(history.Entries))
		for i, e := range history.Entries {
			entries[i] = historyEntryResponse{
				ID:              e.ID,
				TransactionID:   e.TransactionID,
				TransactionType: e.TransactionType,
				Description:     e.Description,
				Direction:       e.Direction,
				Amount:          e.Amount,
				CreatedAt:       e.CreatedAt,
			}
		}

		writeJSON(w, http.StatusOK, historyResponse{WalletID: id.String(), Entries: entries})
	}
}

type topUpRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	Amount         int64  `json:"amount"`
	Description    string `json:"description,omitempty"`
}

type topUpResponse struct {
	TransactionID  string    `json:"transaction_id"`
	WalletID       string    `json:"wallet_id"`
	Amount         int64     `json:"amount"`
	IdempotencyKey string    `json:"idempotency_key"`
	Description    string    `json:"description,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	Replayed       bool      `json:"replayed"`
}

// TopUp handles POST /wallets/{id}/topup:
//
//	{"idempotency_key": "...", "amount": 500, "description": "optional"}
//
// It builds the two-leg transaction ledger-service actually needs
// (debit the external funding account, credit this wallet) -- the
// wallet-service caller only ever thinks in terms of "top up wallet X
// by N", never raw ledger legs.
func TopUp(ledger LedgerClient, idem IdempotencyStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		walletID, ok := parseWalletID(w, r)
		if !ok {
			return
		}

		var req topUpRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed JSON body: "+err.Error())
			return
		}
		if req.IdempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "idempotency_key is required")
			return
		}
		if req.Amount <= 0 {
			writeError(w, http.StatusBadRequest, "amount must be positive")
			return
		}

		txnReq := ledgerclient.PostTransactionRequest{
			IdempotencyKey:  req.IdempotencyKey,
			TransactionType: ledgerclient.TransactionTypeTopUp,
			Description:     req.Description,
			Legs: []ledgerclient.Leg{
				{AccountID: ledgerclient.ExternalFundingAccountID, Direction: ledgerclient.DirectionDebit, Amount: req.Amount},
				{AccountID: walletID.String(), Direction: ledgerclient.DirectionCredit, Amount: req.Amount},
			},
		}

		postTransaction(w, r, ledger, idem, req.IdempotencyKey, txnReq, func(result ledgerclient.PostTransactionResponse) any {
			return topUpResponse{
				TransactionID:  result.Transaction.ID,
				WalletID:       walletID.String(),
				Amount:         req.Amount,
				IdempotencyKey: result.Transaction.IdempotencyKey,
				Description:    result.Transaction.Description,
				CreatedAt:      result.Transaction.CreatedAt,
				Replayed:       result.Replayed,
			}
		})
	}
}

type transferRequest struct {
	IdempotencyKey      string `json:"idempotency_key"`
	DestinationWalletID string `json:"destination_wallet_id"`
	Amount              int64  `json:"amount"`
	Description         string `json:"description,omitempty"`
}

type transferResponse struct {
	TransactionID       string    `json:"transaction_id"`
	SourceWalletID      string    `json:"source_wallet_id"`
	DestinationWalletID string    `json:"destination_wallet_id"`
	Amount              int64     `json:"amount"`
	IdempotencyKey      string    `json:"idempotency_key"`
	Description         string    `json:"description,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	Replayed            bool      `json:"replayed"`
}

// Transfer handles POST /wallets/{id}/transfer:
//
//	{"idempotency_key": "...", "destination_wallet_id": "...", "amount": 500}
//
// It builds the two-leg transaction (debit source, credit destination).
// destination_wallet_id equal to the path {id} is rejected before
// calling ledger-service: ledger-service's netDeltas nets same-account
// legs together (see its post_transaction.go), so a "transfer to
// yourself" would silently net to a zero-balance-change no-op instead
// of a clear error -- worth catching here rather than letting a caller
// discover that the confusing way.
func Transfer(ledger LedgerClient, idem IdempotencyStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sourceID, ok := parseWalletID(w, r)
		if !ok {
			return
		}

		var req transferRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed JSON body: "+err.Error())
			return
		}
		if req.IdempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "idempotency_key is required")
			return
		}
		if req.Amount <= 0 {
			writeError(w, http.StatusBadRequest, "amount must be positive")
			return
		}
		destID, err := uuid.Parse(req.DestinationWalletID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid destination_wallet_id: "+err.Error())
			return
		}
		if destID == sourceID {
			writeError(w, http.StatusBadRequest, "destination_wallet_id must differ from the source wallet")
			return
		}

		txnReq := ledgerclient.PostTransactionRequest{
			IdempotencyKey:  req.IdempotencyKey,
			TransactionType: ledgerclient.TransactionTypeTransfer,
			Description:     req.Description,
			Legs: []ledgerclient.Leg{
				{AccountID: sourceID.String(), Direction: ledgerclient.DirectionDebit, Amount: req.Amount},
				{AccountID: destID.String(), Direction: ledgerclient.DirectionCredit, Amount: req.Amount},
			},
		}

		postTransaction(w, r, ledger, idem, req.IdempotencyKey, txnReq, func(result ledgerclient.PostTransactionResponse) any {
			return transferResponse{
				TransactionID:       result.Transaction.ID,
				SourceWalletID:      sourceID.String(),
				DestinationWalletID: destID.String(),
				Amount:              req.Amount,
				IdempotencyKey:      result.Transaction.IdempotencyKey,
				Description:         result.Transaction.Description,
				CreatedAt:           result.Transaction.CreatedAt,
				Replayed:            result.Replayed,
			}
		})
	}
}

// postTransaction is the fast-path-idempotency + ledger-service-call
// logic shared by TopUp and Transfer -- see CLAUDE.md's "Code reuse"
// rule. Both handlers only differ in how they build the request legs
// and shape the response, which is exactly what buildResponse
// parameterizes.
//
// Flow:
//  1. Check the Redis fast-path cache for idempotencyKey (via
//     lookupIdempotencyCache). On a hit, replay the cached status+body
//     verbatim and return -- ledger-service is never called.
//  2. On a miss (including a Redis error -- see internal/idempotency's
//     package doc), call ledger-service's PostTransaction with req.
//  3. On failure, map and write the error, and deliberately do NOT
//     cache it: a transient failure (e.g. 503 from ledger-service under
//     concurrency contention, or insufficient funds) must remain
//     retryable with the same key, not get permanently frozen into the
//     idempotency cache as if it were the final answer.
//  4. On success, shape the response via buildResponse, write it (201
//     for a freshly-posted transaction, 200 if ledger-service reports
//     the idempotency key had already been used), and best-effort cache
//     it for next time.
func postTransaction(
	w http.ResponseWriter, r *http.Request,
	ledger LedgerClient, idem IdempotencyStore,
	idempotencyKey string, req ledgerclient.PostTransactionRequest,
	buildResponse func(ledgerclient.PostTransactionResponse) any,
) {
	ctx := r.Context()

	if cached, found := lookupIdempotencyCache(ctx, idem, idempotencyKey); found {
		writeReplayedCacheHit(w, cached)
		return
	}

	result, err := ledger.PostTransaction(ctx, req)
	if err != nil {
		writeLedgerClientError(w, err)
		return
	}

	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}

	body := writeJSON(w, status, buildResponse(result))
	if body == nil {
		return // writeJSON already logged the marshal failure.
	}

	cacheEntry := idempotency.CachedResponse{StatusCode: status, Body: body}
	if err := idem.Set(ctx, idempotencyKey, cacheEntry); err != nil {
		log.Printf("handler: failed to cache idempotent response for key %q (fast path only, not fatal): %v", idempotencyKey, err)
	}
}

// writeReplayedCacheHit serves a Redis fast-path cache hit. Reaching
// this function at all already proves this exact idempotency key was
// processed successfully once before -- that's what "hit" means -- so
// the response written here always reports replayed: true and status
// 200, regardless of what got cached. What's cached is the very first
// successful call's own response verbatim, which necessarily had
// replayed: false and status 201 baked in (that's what a brand-new
// transaction's own first response always says about itself); serving
// that byte-for-byte on every later hit would silently repeat a
// now-stale "this is new" claim forever, telling every caller after the
// first one the opposite of what's true. Every other field (transaction
// ID, amount, timestamps, ...) is left exactly as originally cached,
// since those describe the transaction that actually happened and don't
// change no matter how many times this key gets reused.
func writeReplayedCacheHit(w http.ResponseWriter, cached idempotency.CachedResponse) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(cached.Body, &fields); err != nil {
		log.Printf("handler: cached idempotent response body is not valid JSON, serving it verbatim (replayed flag may be stale): %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cached.StatusCode)
		w.Write(cached.Body)
		return
	}
	fields["replayed"] = json.RawMessage("true")
	writeJSON(w, http.StatusOK, fields)
}

// lookupIdempotencyCache checks the fast-path cache for key. Any Redis
// error is logged and treated as a miss -- see internal/idempotency's
// package doc for why that's correct, not a request failure.
func lookupIdempotencyCache(ctx context.Context, idem IdempotencyStore, key string) (idempotency.CachedResponse, bool) {
	cached, found, err := idem.Get(ctx, key)
	if err != nil {
		log.Printf("handler: idempotency cache lookup failed for key %q, falling back to ledger-service: %v", key, err)
		return idempotency.CachedResponse{}, false
	}
	return cached, found
}
