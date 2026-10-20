// Package paypal is a small hand-written client for the PayPal Payouts REST API
// (sandbox only). It is deliberately behind an interface so the engine can run
// against the real sandbox, PayPal's canned test values, or an in-memory fake.
package paypal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type CreateItem struct {
	SenderItemID string
	Receiver     string
	Currency     string
	Value        string
	Note         string
}

type CreateBatchRequest struct {
	SenderBatchID string
	EmailSubject  string
	Items         []CreateItem
}

type ItemInfo struct {
	PayoutItemID      string
	PayoutBatchID     string
	SenderItemID      string
	TransactionStatus string
	Receiver          string
	Currency          string
	Value             string
	ErrorName         string
	ErrorMessage      string
}

type BatchResult struct {
	BatchID       string
	BatchStatus   string
	SenderBatchID string
	Items         []ItemInfo
}

// APIError is a non-2xx response from PayPal.
type APIError struct {
	Status  int
	Name    string
	Message string
	DebugID string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("paypal: HTTP %d %s: %s (debug_id=%s)", e.Status, e.Name, e.Message, e.DebugID)
}

// Retryable reports whether the same request may safely be repeated.
func (e *APIError) Retryable() bool { return e.Status >= 500 || e.Status == 429 }

// Client is the subset of the Payouts API this system needs.
type Client interface {
	CreateBatch(ctx context.Context, req CreateBatchRequest, requestID string) (BatchResult, error)
	GetBatch(ctx context.Context, batchID string) (BatchResult, error)
	GetItem(ctx context.Context, itemID string) (ItemInfo, error)
	CancelItem(ctx context.Context, itemID string) (ItemInfo, error)
	VerifyWebhook(ctx context.Context, h http.Header, body []byte, webhookID string) (bool, error)
}

// HTTPClient talks to the PayPal sandbox.
type HTTPClient struct {
	base, id, secret string
	hc               *http.Client
	mu               sync.Mutex
	token            string
	expires          time.Time
	sleep            func(time.Duration)
	maxAttempts      int
}

func NewHTTPClient(base, id, secret string) *HTTPClient {
	return &HTTPClient{
		base: strings.TrimRight(base, "/"), id: id, secret: secret,
		hc:    &http.Client{Timeout: 30 * time.Second},
		sleep: time.Sleep, maxAttempts: 3,
	}
}

type tokenResp struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// AccessToken returns a cached OAuth token, refreshing it shortly before expiry.
func (c *HTTPClient) AccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires.Add(-60*time.Second)) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.id, c.secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", parseAPIError(resp.StatusCode, b)
	}
	var t tokenResp
	if err := json.Unmarshal(b, &t); err != nil || t.AccessToken == "" {
		return "", errors.New("paypal: malformed token response")
	}
	c.token = t.AccessToken
	c.expires = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return c.token, nil
}

func (c *HTTPClient) invalidate() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

func parseAPIError(status int, body []byte) error {
	var e struct {
		Name    string `json:"name"`
		Message string `json:"message"`
		DebugID string `json:"debug_id"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Name == "" {
		e.Name = http.StatusText(status)
	}
	return &APIError{Status: status, Name: e.Name, Message: e.Message, DebugID: e.DebugID}
}

// do performs a request. Retries reuse the SAME body and PayPal-Request-Id so a
// repeated request can never create a second payout.
func (c *HTTPClient) do(ctx context.Context, method, path string, body any, requestID string, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	var lastErr error
	refreshed := false
	for attempt := 0; attempt < c.maxAttempts; attempt++ {
		if attempt > 0 {
			d := time.Duration(200*(1<<uint(attempt-1))) * time.Millisecond
			c.sleep(d + time.Duration(rand.Intn(100))*time.Millisecond)
		}
		tok, err := c.AccessToken(ctx)
		if err != nil {
			return err
		}
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if requestID != "" {
			req.Header.Set("PayPal-Request-Id", requestID)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && !refreshed {
			refreshed = true
			c.invalidate()
			attempt--
			continue
		}
		if resp.StatusCode/100 != 2 {
			ae := parseAPIError(resp.StatusCode, b).(*APIError)
			if ae.Retryable() {
				lastErr = ae
				continue
			}
			return ae
		}
		if out != nil && len(b) > 0 {
			if err := json.Unmarshal(b, out); err != nil {
				return fmt.Errorf("paypal: decoding response: %w", err)
			}
		}
		return nil
	}
	return lastErr
}

type amountJSON struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

type itemJSON struct {
	PayoutItemID      string `json:"payout_item_id"`
	PayoutBatchID     string `json:"payout_batch_id"`
	TransactionStatus string `json:"transaction_status"`
	PayoutItem        struct {
		Receiver     string     `json:"receiver"`
		SenderItemID string     `json:"sender_item_id"`
		Amount       amountJSON `json:"amount"`
	} `json:"payout_item"`
	Errors *struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (j itemJSON) info() ItemInfo {
	it := ItemInfo{
		PayoutItemID: j.PayoutItemID, PayoutBatchID: j.PayoutBatchID, TransactionStatus: j.TransactionStatus,
		Receiver: j.PayoutItem.Receiver, SenderItemID: j.PayoutItem.SenderItemID,
		Currency: j.PayoutItem.Amount.Currency, Value: j.PayoutItem.Amount.Value,
	}
	if j.Errors != nil {
		it.ErrorName, it.ErrorMessage = j.Errors.Name, j.Errors.Message
	}
	return it
}

type batchJSON struct {
	TotalPages  int `json:"total_pages"`
	BatchHeader struct {
		PayoutBatchID     string `json:"payout_batch_id"`
		BatchStatus       string `json:"batch_status"`
		SenderBatchHeader struct {
			SenderBatchID string `json:"sender_batch_id"`
		} `json:"sender_batch_header"`
	} `json:"batch_header"`
	Items []itemJSON `json:"items"`
}

func (c *HTTPClient) CreateBatch(ctx context.Context, r CreateBatchRequest, requestID string) (BatchResult, error) {
	type itemBody struct {
		RecipientType string     `json:"recipient_type"`
		Amount        amountJSON `json:"amount"`
		Receiver      string     `json:"receiver"`
		Note          string     `json:"note,omitempty"`
		SenderItemID  string     `json:"sender_item_id"`
	}
	body := struct {
		Header struct {
			SenderBatchID string `json:"sender_batch_id"`
			EmailSubject  string `json:"email_subject,omitempty"`
		} `json:"sender_batch_header"`
		Items []itemBody `json:"items"`
	}{}
	body.Header.SenderBatchID, body.Header.EmailSubject = r.SenderBatchID, r.EmailSubject
	for _, it := range r.Items {
		body.Items = append(body.Items, itemBody{"EMAIL", amountJSON{it.Currency, it.Value}, it.Receiver, it.Note, it.SenderItemID})
	}
	var out batchJSON
	if err := c.do(ctx, http.MethodPost, "/v1/payments/payouts", body, requestID, &out); err != nil {
		return BatchResult{}, err
	}
	return BatchResult{BatchID: out.BatchHeader.PayoutBatchID, BatchStatus: out.BatchHeader.BatchStatus, SenderBatchID: r.SenderBatchID}, nil
}

func (c *HTTPClient) GetBatch(ctx context.Context, id string) (BatchResult, error) {
	var res BatchResult
	for page := 1; ; page++ {
		var out batchJSON
		path := fmt.Sprintf("/v1/payments/payouts/%s?page_size=1000&page=%d", url.PathEscape(id), page)
		if err := c.do(ctx, http.MethodGet, path, nil, "", &out); err != nil {
			return BatchResult{}, err
		}
		res.BatchID, res.BatchStatus = out.BatchHeader.PayoutBatchID, out.BatchHeader.BatchStatus
		res.SenderBatchID = out.BatchHeader.SenderBatchHeader.SenderBatchID
		for _, it := range out.Items {
			info := it.info()
			if info.PayoutBatchID == "" {
				info.PayoutBatchID = res.BatchID
			}
			res.Items = append(res.Items, info)
		}
		if page >= out.TotalPages || page >= 50 {
			return res, nil
		}
	}
}

func (c *HTTPClient) GetItem(ctx context.Context, id string) (ItemInfo, error) {
	var out itemJSON
	if err := c.do(ctx, http.MethodGet, "/v1/payments/payouts-item/"+url.PathEscape(id), nil, "", &out); err != nil {
		return ItemInfo{}, err
	}
	return out.info(), nil
}

func (c *HTTPClient) CancelItem(ctx context.Context, id string) (ItemInfo, error) {
	var out itemJSON
	if err := c.do(ctx, http.MethodPost, "/v1/payments/payouts-item/"+url.PathEscape(id)+"/cancel", nil, "", &out); err != nil {
		return ItemInfo{}, err
	}
	return out.info(), nil
}

// VerifyWebhook calls PayPal's verify-webhook-signature endpoint with the raw event.
// Simulator events from the dashboard cannot be verified this way.
func (c *HTTPClient) VerifyWebhook(ctx context.Context, h http.Header, body []byte, webhookID string) (bool, error) {
	if webhookID == "" {
		return false, errors.New("PAYPAL_WEBHOOK_ID is not configured")
	}
	req := map[string]any{
		"auth_algo":         h.Get("PAYPAL-AUTH-ALGO"),
		"cert_url":          h.Get("PAYPAL-CERT-URL"),
		"transmission_id":   h.Get("PAYPAL-TRANSMISSION-ID"),
		"transmission_sig":  h.Get("PAYPAL-TRANSMISSION-SIG"),
		"transmission_time": h.Get("PAYPAL-TRANSMISSION-TIME"),
		"webhook_id":        webhookID,
		"webhook_event":     json.RawMessage(body),
	}
	var out struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/notifications/verify-webhook-signature", req, "", &out); err != nil {
		return false, err
	}
	return out.VerificationStatus == "SUCCESS", nil
}

var errName = regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)

// SimItem reads an item through one of PayPal's documented test values (for
// example ERRPYO047). PayPal answers with a canned response; depending on the
// test value that is either an item payload or an HTTP error whose name is the
// simulated error. Both are turned into an ItemInfo. TODO(verify): confirm which
// shape each test value returns during the day-1 spike.
func SimItem(ctx context.Context, c Client, tag string) (ItemInfo, error) {
	info, err := c.GetItem(ctx, tag)
	if err == nil {
		return info, nil
	}
	var ae *APIError
	if errors.As(err, &ae) && errName.MatchString(ae.Name) && ae.Status < 500 {
		return ItemInfo{PayoutItemID: tag, TransactionStatus: "FAILED", ErrorName: ae.Name, ErrorMessage: ae.Message}, nil
	}
	return ItemInfo{}, err
}
