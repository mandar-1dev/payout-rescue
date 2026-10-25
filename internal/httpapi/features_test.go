package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// raw sends a request and returns the status and body, for responses that are arrays.
func raw(t *testing.T, srv *httptest.Server, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
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
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

type actionRow struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

func proposedActions(t *testing.T, srv *httptest.Server) []actionRow {
	t.Helper()
	code, b := raw(t, srv, "GET", "/api/actions?status=proposed", "tok", nil)
	if code != 200 {
		t.Fatalf("actions: %d %s", code, b)
	}
	var rows []actionRow
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestConflictPackAndGeneratedReferences(t *testing.T) {
	srv := newServer(t)
	if code, _ := raw(t, srv, "POST", "/api/demo/seed-conflicts", "tok", nil); code != 201 {
		t.Fatalf("seed-conflicts: %d", code)
	}
	if code, _ := raw(t, srv, "POST", "/api/demo/seed-conflicts", "tok", nil); code != 409 {
		t.Fatalf("a second conflict seed must be 409, got %d", code)
	}
	if code, _ := raw(t, srv, "GET", "/api/conflicts", "", nil); code != 401 {
		t.Fatalf("conflicts need a token, got %d", code)
	}
	code, b := raw(t, srv, "GET", "/api/conflicts?status=open", "tok", nil)
	var cs []map[string]any
	if code != 200 || json.Unmarshal(b, &cs) != nil || len(cs) != 7 {
		t.Fatalf("expected 7 open conflicts, got %d %s", code, b)
	}
	// Resolving through the API needs a name and an allowed resolution.
	id := cs[0]["id"].(string)
	if code, _ := raw(t, srv, "POST", "/api/conflicts/"+id+"/resolve", "tok", map[string]any{"resolution": "nonsense", "approver": "Ana"}); code != 400 {
		t.Fatalf("a made-up resolution must be refused, got %d", code)
	}
	if code, _ := raw(t, srv, "POST", "/api/conflicts/"+id+"/resolve", "tok", map[string]any{"resolution": "dismiss"}); code != 400 {
		t.Fatalf("a missing name must be refused, got %d", code)
	}

	_, dp := do(t, srv, "GET", "/api/demo/payouts", "tok", nil)
	if s, _ := dp["standard"].([]any); len(s) != 12 {
		t.Fatalf("standard pack: %v", dp["standard"])
	}
	if c, _ := dp["conflicts"].([]any); len(c) != 7 {
		t.Fatalf("conflict pack: %v", dp["conflicts"])
	}
	_, ref := do(t, srv, "GET", "/api/reference", "tok", nil)
	if m, _ := ref["rules_matrix"].([]any); len(m) < 30 {
		t.Fatalf("rules matrix too small: %d", len(m))
	}
}

func TestSnoozeBulkAcknowledgeAndNotes(t *testing.T) {
	srv := newServer(t)
	if code, b := raw(t, srv, "POST", "/api/batches", "tok", map[string]any{"mode": "FAKE", "recipients": []map[string]any{
		{"name": "Bulk One", "email": "bulk.one@example.com", "amount": "10.00"},
		{"name": "Bulk Two", "email": "not-an-email", "amount": "11.00"},
	}}); code != 201 {
		t.Fatalf("create batch: %d %s", code, b)
	}
	acts := proposedActions(t, srv)
	var notify, other []string
	for _, a := range acts {
		if a.Kind == "notify" {
			notify = append(notify, a.ID)
		} else {
			other = append(other, a.ID)
		}
	}
	if len(notify) < 2 {
		t.Fatalf("expected drafted messages, got %+v", acts)
	}

	// Snooze: only 1, 3 or 7 days, and the action leaves the queue.
	if code, _ := raw(t, srv, "POST", "/api/actions/"+notify[0]+"/snooze", "tok", map[string]any{"days": 2}); code != 400 {
		t.Fatalf("2 days must be refused, got %d", code)
	}
	if code, b := raw(t, srv, "POST", "/api/actions/"+notify[0]+"/snooze", "tok", map[string]any{"days": 3}); code != 200 {
		t.Fatalf("snooze: %d %s", code, b)
	}
	for _, a := range proposedActions(t, srv) {
		if a.ID == notify[0] {
			t.Fatal("a snoozed action must be hidden from the queue")
		}
	}
	if code, _ := raw(t, srv, "POST", "/api/actions/00000000-0000-0000-0000-000000000000/snooze", "tok", map[string]any{"days": 1}); code != 404 {
		t.Fatalf("an unknown action must be 404, got %d", code)
	}

	// Bulk acknowledge works for messages and refuses anything that moves money.
	if code, _ := raw(t, srv, "POST", "/api/actions/bulk-acknowledge", "tok", map[string]any{"action_ids": notify[1:], "approver": ""}); code != 400 {
		t.Fatalf("bulk acknowledge needs a name, got %d", code)
	}
	if code, b := raw(t, srv, "POST", "/api/actions/bulk-acknowledge", "tok", map[string]any{"action_ids": notify[1:], "approver": "Ana"}); code != 200 || !strings.Contains(string(b), "acknowledged") {
		t.Fatalf("bulk acknowledge: %d %s", code, b)
	}

	// A money-moving proposal can never be bulk-approved. Create one: reply with a corrected email.
	_, items := raw(t, srv, "GET", "/api/items?q=Bulk+Two", "tok", nil)
	var rows []map[string]any
	_ = json.Unmarshal(items, &rows)
	if len(rows) != 1 {
		t.Fatalf("items: %s", items)
	}
	itemID := rows[0]["id"].(string)
	if code, b := raw(t, srv, "POST", "/api/items/"+itemID+"/corrected-email", "tok", map[string]any{"email": "bulk.two.fixed@example.com", "actor": "Ana"}); code != 200 {
		t.Fatalf("corrected email: %d %s", code, b)
	}
	var reissue string
	for _, a := range proposedActions(t, srv) {
		if a.Kind == "reissue" {
			reissue = a.ID
		}
	}
	if reissue == "" {
		t.Fatal("expected a replacement proposal")
	}
	if code, _ := raw(t, srv, "POST", "/api/actions/bulk-acknowledge", "tok", map[string]any{"action_ids": []string{reissue}, "approver": "Ana"}); code != 400 {
		t.Fatalf("a money-moving action must never be bulk-approved, got %d", code)
	}
	_, ib := raw(t, srv, "GET", "/api/items", "tok", nil)
	_ = json.Unmarshal(ib, &rows)
	if len(rows) != 2 {
		t.Fatalf("the refused bulk call must not create a replacement; found %d items", len(rows))
	}
	_ = other

	// Notes: validated, audited, listed with the item.
	if code, _ := raw(t, srv, "POST", "/api/items/"+itemID+"/notes", "tok", map[string]any{"body": "   "}); code != 400 {
		t.Fatalf("an empty note must be refused, got %d", code)
	}
	if code, _ := raw(t, srv, "POST", "/api/items/00000000-0000-0000-0000-000000000000/notes", "tok", map[string]any{"body": "x"}); code != 404 {
		t.Fatalf("a note on an unknown payout must be 404, got %d", code)
	}
	if code, b := raw(t, srv, "POST", "/api/items/"+itemID+"/notes", "tok", map[string]any{"body": "Called the recipient", "author": "Ana"}); code != 201 {
		t.Fatalf("note: %d %s", code, b)
	}
	_, one := do(t, srv, "GET", "/api/items/"+itemID, "tok", nil)
	if n, _ := one["notes"].([]any); len(n) != 1 {
		t.Fatalf("the note must come back with the item: %v", one["notes"])
	}
}

func TestAuditCSVExportIsFormulaSafe(t *testing.T) {
	for _, v := range []string{"=HYPERLINK(\"http://evil\")", "+1", "-1", "@SUM(A1)"} {
		if !strings.HasPrefix(csvSafe(v), "'") {
			t.Fatalf("%q not neutralised", v)
		}
	}
	if csvSafe("normal") != "normal" || csvSafe("") != "" {
		t.Fatal("normal text must be unchanged")
	}
	srv := newServer(t)
	_, _ = raw(t, srv, "POST", "/api/batches", "tok", map[string]any{"mode": "FAKE", "recipients": []map[string]any{{"name": "=cmd", "email": "csv@example.com", "amount": "5.00"}}})
	code, b := raw(t, srv, "GET", "/api/audit/export.csv", "tok", nil)
	if code != 200 || !strings.HasPrefix(string(b), "id,timestamp,actor,event") {
		t.Fatalf("csv: %d %.80s", code, b)
	}
}

func TestDuplicateCheck(t *testing.T) {
	srv := newServer(t)
	_, _ = raw(t, srv, "POST", "/api/batches", "tok", map[string]any{"mode": "FAKE", "recipients": []map[string]any{{"name": "Dup Person", "email": "dup.person@example.com", "amount": "42.00"}}})
	_, out := do(t, srv, "POST", "/api/batches/check", "tok", map[string]any{"recipients": []map[string]any{
		{"email": "dup.person@example.com", "amount": "42.00", "currency": "USD"},
		{"email": "dup.person@example.com", "amount": "43.00", "currency": "USD"},
		{"email": "someone.else@example.com", "amount": "42.00", "currency": "USD"},
	}})
	if d, _ := out["duplicates"].([]any); len(d) != 1 {
		t.Fatalf("expected exactly one duplicate warning, got %v", out)
	}
}
