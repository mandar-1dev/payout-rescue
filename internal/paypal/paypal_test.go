package paypal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newTest(t *testing.T, h http.HandlerFunc) (*HTTPClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewHTTPClient(srv.URL, "id", "secret")
	c.sleep = func(time.Duration) {}
	return c, srv
}

func tokenOK(w http.ResponseWriter) {
	w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
}

func TestTokenCachedAndRetrySameRequestID(t *testing.T) {
	var tokenCalls, createCalls int32
	var ids []string
	c, _ := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth2/token":
			atomic.AddInt32(&tokenCalls, 1)
			tokenOK(w)
		case "/v1/payments/payouts":
			n := atomic.AddInt32(&createCalls, 1)
			ids = append(ids, r.Header.Get("PayPal-Request-Id"))
			if n == 1 {
				w.WriteHeader(500)
				w.Write([]byte(`{"name":"INTERNAL_ERROR","message":"boom"}`))
				return
			}
			body, _ := io.ReadAll(r.Body)
			var got map[string]any
			json.Unmarshal(body, &got)
			if got["sender_batch_header"].(map[string]any)["sender_batch_id"] != "sb1" {
				t.Errorf("bad body %s", body)
			}
			w.WriteHeader(201)
			w.Write([]byte(`{"batch_header":{"payout_batch_id":"B1","batch_status":"PENDING"}}`))
		}
	})
	res, err := c.CreateBatch(context.Background(), CreateBatchRequest{SenderBatchID: "sb1", Items: []CreateItem{{SenderItemID: "i1", Receiver: "a@b.co", Currency: "USD", Value: "1.00"}}}, "req-1")
	if err != nil || res.BatchID != "B1" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if createCalls != 2 || ids[0] != "req-1" || ids[1] != "req-1" {
		t.Fatalf("retry must reuse the request id: calls=%d ids=%v", createCalls, ids)
	}
	if _, err := c.GetBatch(context.Background(), "B1"); err == nil {
		// second endpoint not served: just ensure token was cached
	}
	if tokenCalls != 1 {
		t.Fatalf("token should be cached, calls=%d", tokenCalls)
	}
}

func TestNoRetryOn4xxAndAPIErrorParsing(t *testing.T) {
	var calls int32
	c, _ := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			tokenOK(w)
			return
		}
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(422)
		w.Write([]byte(`{"name":"ITEM_INCORRECT_STATUS","message":"nope","debug_id":"d1"}`))
	})
	_, err := c.CancelItem(context.Background(), "X")
	ae, ok := err.(*APIError)
	if !ok || ae.Name != "ITEM_INCORRECT_STATUS" || ae.DebugID != "d1" || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestGetBatchPaginationAndErrors(t *testing.T) {
	c, _ := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			tokenOK(w)
			return
		}
		page := r.URL.Query().Get("page")
		item := `{"payout_item_id":"I` + page + `","transaction_status":"FAILED","payout_item":{"receiver":"a@b.co","sender_item_id":"s` + page + `","amount":{"currency":"USD","value":"2.00"}},"errors":{"name":"INVALID_EMAIL","message":"bad"}}`
		w.Write([]byte(`{"total_pages":2,"batch_header":{"payout_batch_id":"B1","batch_status":"SUCCESS","sender_batch_header":{"sender_batch_id":"sb"}},"items":[` + item + `]}`))
	})
	res, err := c.GetBatch(context.Background(), "B1")
	if err != nil || len(res.Items) != 2 || res.Items[1].PayoutItemID != "I2" || res.Items[0].ErrorName != "INVALID_EMAIL" || res.Items[0].PayoutBatchID != "B1" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestVerifyWebhookSendsRawEvent(t *testing.T) {
	c, _ := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			tokenOK(w)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var got map[string]any
		json.Unmarshal(b, &got)
		if got["webhook_id"] != "WH1" || got["transmission_id"] != "T1" || got["webhook_event"].(map[string]any)["id"] != "EV1" {
			t.Errorf("bad verify body %s", b)
		}
		w.Write([]byte(`{"verification_status":"SUCCESS"}`))
	})
	h := http.Header{}
	h.Set("PAYPAL-TRANSMISSION-ID", "T1")
	ok, err := c.VerifyWebhook(context.Background(), h, []byte(`{"id":"EV1"}`), "WH1")
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if ok, _ := c.VerifyWebhook(context.Background(), h, []byte(`{}`), ""); ok {
		t.Fatal("missing webhook id must not verify")
	}
}

func TestSimItemConvertsNamedErrors(t *testing.T) {
	c, _ := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			tokenOK(w)
			return
		}
		w.WriteHeader(400)
		w.Write([]byte(`{"name":"RECEIVER_UNREGISTERED","message":"simulated"}`))
	})
	info, err := SimItem(context.Background(), c, "ERRPYO047")
	if err != nil || info.TransactionStatus != "FAILED" || info.ErrorName != "RECEIVER_UNREGISTERED" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
}
