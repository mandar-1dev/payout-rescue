package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mandar-1dev/payoutrescue/internal/config"
	"github.com/mandar-1dev/payoutrescue/internal/engine"
	"github.com/mandar-1dev/payoutrescue/internal/fakepaypal"
	"github.com/mandar-1dev/payoutrescue/internal/sse"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, `TRUNCATE conflicts, notes, bench_runs, messages, actions, webhook_events, audit_log, payout_items, payout_batches, logical_payouts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	hub := sse.New()
	eng := &engine.Engine{Store: st, Fake: fakepaypal.New(), Platform: "Test", Notify: hub.Publish}
	static := fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}, "assets/app.js": {Data: []byte("x")}}
	s := &Server{Eng: eng, Cfg: config.Config{AdminToken: "tok", FakeEnabled: true, Platform: "Test"}, Hub: hub, Static: static}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestHealthAuthAndSPA(t *testing.T) {
	srv := newServer(t)
	if r, _ := do(t, srv, "GET", "/healthz", "", nil); r.StatusCode != 200 {
		t.Fatalf("healthz %d", r.StatusCode)
	}
	if r, _ := do(t, srv, "GET", "/readyz", "", nil); r.StatusCode != 200 {
		t.Fatalf("readyz %d", r.StatusCode)
	}
	if r, _ := do(t, srv, "GET", "/api/limbo", "", nil); r.StatusCode != 401 {
		t.Fatalf("missing token must be 401, got %d", r.StatusCode)
	}
	if r, _ := do(t, srv, "GET", "/api/limbo", "wrong", nil); r.StatusCode != 401 {
		t.Fatalf("wrong token must be 401, got %d", r.StatusCode)
	}
	if r, _ := do(t, srv, "GET", "/api/limbo", "tok", nil); r.StatusCode != 200 {
		t.Fatalf("valid token must pass, got %d", r.StatusCode)
	}
	if r, _ := do(t, srv, "GET", "/api/nope", "tok", nil); r.StatusCode != 404 {
		t.Fatalf("unknown API route must be 404, got %d", r.StatusCode)
	}
	resp, err := http.Get(srv.URL + "/items/some-deep-link")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(buf.String(), "app") {
		t.Fatalf("deep links must fall back to index.html, got %d %q", resp.StatusCode, buf.String())
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("missing CSP: %q", csp)
	}
}

func TestBatchValidationApprovalAndWebhook(t *testing.T) {
	srv := newServer(t)
	if r, out := do(t, srv, "POST", "/api/batches", "tok", map[string]any{"mode": "FAKE", "recipients": []map[string]string{{"name": "A", "email": "a@x.co", "amount": "-5"}}}); r.StatusCode != 400 {
		t.Fatalf("a negative amount must be refused: %d %v", r.StatusCode, out)
	}
	if r, _ := do(t, srv, "POST", "/api/batches", "tok", map[string]any{"mode": "LIVE_SANDBOX", "recipients": []map[string]string{{"name": "A", "email": "a@x.co", "amount": "5"}}}); r.StatusCode != 409 {
		t.Fatalf("live mode without credentials must be 409, got %d", r.StatusCode)
	}
	r, out := do(t, srv, "POST", "/api/batches", "tok", map[string]any{"mode": "FAKE", "recipients": []map[string]string{{"name": "Dana", "email": "bad-email", "amount": "10.00"}}})
	if r.StatusCode != 201 {
		t.Fatalf("create: %d %v", r.StatusCode, out)
	}
	_, items := do(t, srv, "GET", "/api/items", "tok", nil)
	_ = items
	resp, err := http.Get(srv.URL + "/api/items")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated item list must be 401, got %d", resp.StatusCode)
	}
	if r, _ := do(t, srv, "POST", "/api/webhooks/paypal", "", map[string]any{"id": "X", "event_type": "Y"}); r.StatusCode != 401 {
		t.Fatalf("unverifiable webhook must be 401, got %d", r.StatusCode)
	}
	if r, _ := do(t, srv, "POST", "/api/webhooks/paypal", "", map[string]any{"nope": 1}); r.StatusCode != 400 {
		t.Fatalf("garbage webhook must be 400, got %d", r.StatusCode)
	}
	if r, _ := do(t, srv, "POST", "/api/actions/00000000-0000-0000-0000-000000000000/approve", "tok", map[string]any{"approver": "x"}); r.StatusCode != 404 {
		t.Fatalf("unknown action must be 404, got %d", r.StatusCode)
	}
}

func TestConflictsEndpointsAndDescriptions(t *testing.T) {
	srv := newServer(t)

	// 1. Conflicts endpoints require token
	if r, _ := do(t, srv, "GET", "/api/conflicts", "", nil); r.StatusCode != 401 {
		t.Fatalf("unauthenticated GET /api/conflicts must be 401, got %d", r.StatusCode)
	}
	if r, _ := do(t, srv, "GET", "/api/conflicts/00000000-0000-0000-0000-000000000000", "", nil); r.StatusCode != 401 {
		t.Fatalf("unauthenticated GET /api/conflicts/{id} must be 401, got %d", r.StatusCode)
	}
	if r, _ := do(t, srv, "POST", "/api/conflicts/00000000-0000-0000-0000-000000000000/resolve", "", nil); r.StatusCode != 401 {
		t.Fatalf("unauthenticated POST /api/conflicts/{id}/resolve must be 401, got %d", r.StatusCode)
	}

	// 2. Seed-conflicts returns 201 the first time, 409 the second time
	r, out := do(t, srv, "POST", "/api/demo/seed-conflicts", "tok", nil)
	if r.StatusCode != 201 {
		t.Fatalf("first seed-conflicts must be 201, got %d: %v", r.StatusCode, out)
	}
	r2, out2 := do(t, srv, "POST", "/api/demo/seed-conflicts", "tok", nil)
	if r2.StatusCode != 409 {
		t.Fatalf("second seed-conflicts must be 409, got %d: %v", r2.StatusCode, out2)
	}

	// 3. Every route in reference has a non-empty description
	rRef, outRef := do(t, srv, "GET", "/api/reference", "tok", nil)
	if rRef.StatusCode != 200 {
		t.Fatalf("GET /api/reference failed: %d", rRef.StatusCode)
	}
	routesRaw, ok := outRef["routes"].([]any)
	if !ok || len(routesRaw) == 0 {
		t.Fatalf("expected non-empty routes in reference: %v", outRef)
	}
	for _, raw := range routesRaw {
		rt := raw.(map[string]any)
		pat, _ := rt["pattern"].(string)
		doc, _ := rt["doc"].(string)
		if strings.TrimSpace(doc) == "" {
			t.Errorf("route %q has empty description", pat)
		}
	}
}
