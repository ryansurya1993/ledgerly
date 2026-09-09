package ledgerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultTimeout bounds how long a single call to ledger-service can
// take when the caller didn't supply its own *http.Client. Without a
// timeout, a wedged or unreachable ledger-service would hang a
// wallet-service request (and the goroutine serving it) indefinitely.
const defaultTimeout = 10 * time.Second

// Client is a thin HTTP client for ledger-service's API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New wraps baseURL (e.g. "http://ledger-service:8080") in a Client. A
// nil httpClient gets a default one with defaultTimeout; pass an
// explicit one to override that (e.g. in tests, or to tune timeouts
// for production).
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}
}

// CreateAccount calls ledger-service's POST /accounts.
func (c *Client) CreateAccount(ctx context.Context, req CreateAccountRequest) (Account, error) {
	var account Account
	err := c.do(ctx, http.MethodPost, "/accounts", req, &account)
	return account, err
}

// PostTransaction calls ledger-service's POST /transactions.
func (c *Client) PostTransaction(ctx context.Context, req PostTransactionRequest) (PostTransactionResponse, error) {
	var resp PostTransactionResponse
	err := c.do(ctx, http.MethodPost, "/transactions", req, &resp)
	return resp, err
}

// GetBalance calls ledger-service's GET /accounts/{id}/balance.
func (c *Client) GetBalance(ctx context.Context, accountID string) (Balance, error) {
	var balance Balance
	err := c.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(accountID)+"/balance", nil, &balance)
	return balance, err
}

// GetHistory calls ledger-service's GET /accounts/{id}/history.
func (c *Client) GetHistory(ctx context.Context, accountID string) (History, error) {
	var history History
	err := c.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(accountID)+"/history", nil, &history)
	return history, err
}

// GetIntegrity calls ledger-service's GET /integrity -- every
// account's drift check, not just one wallet's.
func (c *Client) GetIntegrity(ctx context.Context) (IntegrityResponse, error) {
	var integrity IntegrityResponse
	err := c.do(ctx, http.MethodGet, "/integrity", nil, &integrity)
	return integrity, err
}

// ListAccounts calls ledger-service's GET /accounts -- every wallet
// account (ledger-service already excludes system accounts like the
// external funding account; see its Ledger.ListWalletAccounts doc
// comment), not just one.
func (c *Client) ListAccounts(ctx context.Context) ([]Account, error) {
	var accounts []Account
	err := c.do(ctx, http.MethodGet, "/accounts", nil, &accounts)
	return accounts, err
}

// do performs one HTTP round trip against ledger-service: marshals
// reqBody (if any) as the request body, and on a 2xx response unmarshals
// the response body into respBody (if non-nil). A non-2xx response is
// decoded as ledger-service's {"error": "..."} shape and returned as
// *APIError; any other failure (couldn't reach ledger-service at all,
// malformed response) is returned as a plain wrapped error, which
// callers in internal/handler treat as "ledger-service unavailable"
// (502) rather than trying to map a specific status code out of it.
func (c *Client) do(ctx context.Context, method, path string, reqBody, respBody any) error {
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call ledger-service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiErrorFromResponse(resp)
	}

	if respBody != nil {
		if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
			return fmt.Errorf("decode ledger-service response: %w", err)
		}
	}
	return nil
}

func apiErrorFromResponse(resp *http.Response) *APIError {
	message := fmt.Sprintf("ledger-service returned status %d", resp.StatusCode)

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil && body.Error != "" {
		message = body.Error
	}

	return &APIError{StatusCode: resp.StatusCode, Message: message}
}
