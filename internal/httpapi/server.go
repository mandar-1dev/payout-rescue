// Package httpapi serves the JSON API, the PayPal webhook, SSE and the embedded SPA.
package httpapi

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mandar-1dev/payoutrescue/internal/ai"
	"github.com/mandar-1dev/payoutrescue/internal/bench"
	"github.com/mandar-1dev/payoutrescue/internal/config"
	"github.com/mandar-1dev/payoutrescue/internal/demo"
	"github.com/mandar-1dev/payoutrescue/internal/engine"
	"github.com/mandar-1dev/payoutrescue/internal/paypal"
	"github.com/mandar-1dev/payoutrescue/internal/rules"
	"github.com/mandar-1dev/payoutrescue/internal/sse"
	"github.com/mandar-1dev/payoutrescue/internal/state"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

type RouteDoc struct {
	Pattern string `json:"pattern"`
	Doc     string `json:"doc"`
}

type Server struct {
	Eng     *engine.Engine
	Cfg     config.Config
	Hub     *sse.Hub
	Static  fs.FS
	Live    paypal.Client
	Log     *slog.Logger
	benchOn atomic.Bool
	mux     *http.ServeMux
	routes  []RouteDoc
}

func (s *Server) route(pattern, doc string, handler http.HandlerFunc) {
	if strings.TrimSpace(doc) == "" {
		panic(fmt.Sprintf("route %q registered without documentation", pattern))
	}
	s.routes = append(s.routes, RouteDoc{Pattern: pattern, Doc: doc})
	s.mux.HandleFunc(pattern, handler)
}

func (s *Server) Routes() []RouteDoc {
	return s.routes
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func (s *Server) tokenOK(r *http.Request) bool {
	if s.Cfg.AdminToken == "" {
		return true // development only; production requires a token (see config)
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" {
		got = r.URL.Query().Get("token") // EventSource cannot set headers
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.Cfg.AdminToken)) == 1
}

func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenOK(r) {
			writeErr(w, http.StatusUnauthorized, "missing or invalid admin token")
			return
		}
		h(w, r)
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, engine.ErrNoClient):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, engine.ErrNotApprovable):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, engine.ErrWillRetry):
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "approved", "message": err.Error()})
	default:
		s.log().Warn("request refused", "err", err)
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

// ---------- routes ----------

func (s *Server) Handler() http.Handler {
	s.mux = http.NewServeMux()
	s.routes = nil

	s.route("GET /healthz", "Liveness probe returning 200 OK", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	s.route("GET /readyz", "Readiness probe verifying database connectivity", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := s.Eng.Store.DB.PingContext(ctx); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "database not ready")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	s.route("GET /api/public", "Public configuration: authentication requirements and platform details", s.public)
	s.route("POST /api/webhooks/paypal", "PayPal webhook receiver for asynchronous payment notifications", s.webhook)

	s.route("GET /api/session", "Current operator session, feature flags, and environment details", s.authed(s.session))
	s.route("GET /api/limbo", "Dashboard summary metrics, aging buckets, and in-limbo currency totals", s.authed(s.limbo))
	s.route("POST /api/batches", "Create a new batch of payout items", s.authed(s.createBatch))
	s.route("GET /api/batches", "List recent payout batches", s.authed(s.listBatches))
	s.route("GET /api/batches/{id}", "Get batch details and item statuses", s.authed(s.getBatch))
	s.route("GET /api/items", "List and filter payout items", s.authed(s.listItems))
	s.route("GET /api/items/{id}", "Get payout item details, audit history, actions, conflicts, and notes", s.authed(s.getItem))
	s.route("POST /api/items/{id}/refresh", "Fetch fresh status from PayPal for an item", s.authed(s.refreshItem))
	s.route("POST /api/items/{id}/corrected-email", "Record a validated corrected recipient email", s.authed(s.correctedEmail))
	s.route("POST /api/items/{id}/notes", "Add an operator note to a payout item", s.authed(s.createNote))
	s.route("GET /api/actions", "List proposed and executed actions", s.authed(s.listActions))
	s.route("POST /api/actions/{id}/approve", "Approve a proposed action (requires approver name)", s.authed(s.approve))
	s.route("POST /api/actions/{id}/reject", "Reject a proposed action", s.authed(s.reject))
	s.route("POST /api/actions/{id}/snooze", "Snooze a proposed action for 1, 3, or 7 days", s.authed(s.snoozeAction))
	s.route("POST /api/actions/bulk-acknowledge", "Bulk acknowledge non-money-moving actions", s.authed(s.bulkAcknowledge))
	s.route("GET /api/conflicts", "List conflicts filtered by status (open/resolved)", s.authed(s.listConflicts))
	s.route("GET /api/conflicts/{id}", "Get details of a single conflict", s.authed(s.getConflict))
	s.route("POST /api/conflicts/{id}/resolve", "Resolve a conflict with a validated resolution and approver name", s.authed(s.resolveConflict))
	s.route("POST /api/messages/inbound", "Ingest an inbound or simulated recipient reply message", s.authed(s.inbound))
	s.route("POST /api/admin/reconcile", "Trigger background reconciliation pass immediately", s.authed(s.reconcile))
	s.route("POST /api/admin/advance", "Advance simulated time for FAKE items to test expiry windows", s.authed(s.advance))
	s.route("POST /api/demo/seed", "Seed standard demo payouts", s.authed(s.seed))
	s.route("GET /api/demo/payouts", "Every demo payout (standard and conflict packs) with its story and what to try.", s.authed(s.demoPayouts))
	s.route("POST /api/batches/check", "Check proposed recipients for an earlier payout with the same email, amount and currency in the last 7 days.", s.authed(s.checkDuplicates))
	s.route("POST /api/demo/seed-conflicts", "Seed 7 conflict demo payouts with open conflicts", s.authed(s.seedConflicts))
	s.route("GET /api/audit", "Query the append-only audit log", s.authed(s.audit))
	s.route("GET /api/audit/verify", "Verify cryptographic hash chain integrity of the audit log", s.authed(s.auditVerify))
	s.route("GET /api/audit/export.csv", "Export the complete audit log in CSV format", s.authed(s.exportAuditCSV))
	s.route("GET /api/reference", "System reference dictionary of routes, statuses, rules, and templates", s.authed(s.reference))
	s.route("POST /api/bench/run", "Start an automated benchmark run", s.authed(s.benchRun))
	s.route("GET /api/bench/latest", "Get the latest benchmark run results", s.authed(s.benchLatest))
	s.route("GET /api/events", "Server-Sent Events stream for real-time dashboard updates", s.authed(s.Hub.ServeHTTP))

	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, http.StatusNotFound, "unknown API route") })
	s.mux.HandleFunc("/", s.spa)
	return s.middleware(s.mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" && r.URL.Path != "/api/events" {
			s.log().Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ---------- SPA ----------

func (s *Server) spa(w http.ResponseWriter, r *http.Request) {
	if s.Static == nil {
		http.NotFound(w, r)
		return
	}
	p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	if fi, err := fs.Stat(s.Static, p); err != nil || fi.IsDir() {
		p = "index.html"
	}
	if _, err := fs.Stat(s.Static, p); err != nil {
		http.Error(w, "frontend not built: run `npm run build` in web/", http.StatusNotFound)
		return
	}
	if strings.HasPrefix(p, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, s.Static, p)
}

// ---------- handlers ----------

func (s *Server) public(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"auth_required": s.Cfg.AdminToken != "", "platform": s.Cfg.Platform, "env": s.Cfg.Env})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	llm := "none"
	if s.Eng.AI != nil {
		llm = s.Cfg.LLMProvider + " / " + s.Cfg.LLMModel
	}
	hook := ""
	if s.Cfg.PublicBaseURL != "" {
		hook = s.Cfg.PublicBaseURL + "/api/webhooks/paypal"
	}
	writeJSON(w, 200, map[string]any{
		"env": s.Cfg.Env, "platform": s.Cfg.Platform, "live_configured": s.Cfg.LiveConfigured(), "fake_enabled": s.Cfg.FakeEnabled,
		"llm": llm, "webhook_bypass": s.Cfg.WebhookBypass, "webhook_url": hook, "webhook_id_set": s.Cfg.PayPalWebhookID != "",
		"sim_tags": engine.SimTags, "templates": ai.TemplateKeys(), "reconcile_seconds": int(s.Cfg.ReconcileInterval.Seconds()),
	})
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	id, dup, err := s.Eng.IngestWebhook(r.Context(), r.Header, body)
	switch {
	case errors.Is(err, engine.ErrBadEvent):
		writeErr(w, http.StatusBadRequest, "not a PayPal event")
	case errors.Is(err, engine.ErrBadSignature):
		s.log().Warn("webhook rejected: signature not verified", "event", id)
		writeErr(w, http.StatusUnauthorized, "signature not verified")
	case err != nil:
		s.log().Error("webhook store failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not store event") // PayPal will retry
	case dup:
		writeJSON(w, 200, map[string]string{"status": "duplicate"})
	default:
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := s.Eng.ProcessEvent(ctx, id); err != nil {
				s.log().Warn("event processing deferred to the reconciler", "event", id, "err", err)
			}
		}()
		writeJSON(w, 200, map[string]string{"status": "accepted"})
	}
}

func (s *Server) limbo(w http.ResponseWriter, r *http.Request) {
	sum, err := s.Eng.Summary(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, sum)
}

func (s *Server) createBatch(w http.ResponseWriter, r *http.Request) {
	var in engine.CreateBatchInput
	if !decode(w, r, &in) {
		return
	}
	b, items, err := s.Eng.CreateBatch(r.Context(), in)
	if errors.Is(err, engine.ErrWillRetry) {
		writeJSON(w, http.StatusAccepted, map[string]any{"batch": b, "items": items, "message": err.Error()})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"batch": b, "items": items})
}

func (s *Server) listBatches(w http.ResponseWriter, r *http.Request) {
	bs, err := store.ListBatches(r.Context(), s.Eng.Store.DB, 100)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, bs)
}

func (s *Server) getBatch(w http.ResponseWriter, r *http.Request) {
	b, err := store.GetBatch(r.Context(), s.Eng.Store.DB, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	items, err := store.ListItems(r.Context(), s.Eng.Store.DB, store.ItemFilter{BatchID: b.ID, Limit: 500})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"batch": b, "items": items})
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ItemFilter{Mode: q.Get("mode"), Search: q.Get("q"), BatchID: q.Get("batch"), Limit: 1000}
	if st := q.Get("status"); st != "" {
		f.Statuses = strings.Split(strings.ToUpper(st), ",")
	}
	items, err := store.ListItems(r.Context(), s.Eng.Store.DB, f)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Attach the number of open actions per item for the grid.
	open := map[string]int{}
	if as, err := store.ListActions(r.Context(), s.Eng.Store.DB, "proposed", "", 2000); err == nil {
		for _, a := range as {
			open[a.ItemID]++
		}
	}
	type row struct {
		store.Item
		OpenActions int `json:"open_actions"`
	}
	out := make([]row, 0, len(items))
	for _, it := range items {
		out = append(out, row{it, open[it.ID]})
	}
	writeJSON(w, 200, out)
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	it, err := store.GetItem(ctx, s.Eng.Store.DB, id, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	actions, _ := store.ListActions(ctx, s.Eng.Store.DB, "", id, 100)
	msgs, _ := store.ListMessages(ctx, s.Eng.Store.DB, id)
	aud, _ := store.ListAudit(ctx, s.Eng.Store.DB, id, 100)
	pre := map[string]engine.Preflight{}
	for _, a := range actions {
		if a.Status == "proposed" && rules.Kind(a.Kind).MoneyMoving() {
			if pf, err := s.Eng.Preflight(ctx, a); err == nil {
				pre[a.ID] = pf
			}
		}
	}
	var history []store.Item
	if all, err := store.ListItems(ctx, s.Eng.Store.DB, store.ItemFilter{Limit: 2000}); err == nil {
		for _, x := range all {
			if x.LogicalPayoutID == it.LogicalPayoutID {
				history = append(history, x)
			}
		}
	}
	confls, _ := store.ListConflictsByItem(ctx, s.Eng.Store.DB, id)
	notes, _ := store.ListNotes(ctx, s.Eng.Store.DB, id)
	writeJSON(w, 200, map[string]any{
		"item": it, "actions": actions, "messages": msgs, "audit": aud,
		"preflight": pre, "attempts": history, "conflicts": confls, "notes": notes,
	})
}

func (s *Server) refreshItem(w http.ResponseWriter, r *http.Request) {
	if err := s.Eng.ProcessItem(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "refreshed"})
}

func (s *Server) correctedEmail(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Actor string }
	if !decode(w, r, &in) {
		return
	}
	if in.Actor == "" {
		in.Actor = "operator"
	}
	if err := s.Eng.SetCorrectedEmail(r.Context(), r.PathValue("id"), in.Email, in.Actor); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "recorded"})
}

func (s *Server) listActions(w http.ResponseWriter, r *http.Request) {
	as, err := store.ListActions(r.Context(), s.Eng.Store.DB, r.URL.Query().Get("status"), r.URL.Query().Get("item"), 300)
	if err != nil {
		s.fail(w, err)
		return
	}
	type row struct {
		store.Action
		Item      *store.Item       `json:"item"`
		Preflight *engine.Preflight `json:"preflight,omitempty"`
	}
	out := make([]row, 0, len(as))
	for _, a := range as {
		rw := row{Action: a}
		if it, err := store.GetItem(r.Context(), s.Eng.Store.DB, a.ItemID, false); err == nil {
			rw.Item = it
		}
		if a.Status == "proposed" && rules.Kind(a.Kind).MoneyMoving() {
			if pf, err := s.Eng.Preflight(r.Context(), a); err == nil {
				rw.Preflight = &pf
			}
		}
		out = append(out, rw)
	}
	writeJSON(w, 200, out)
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	var in struct{ Approver string }
	if !decode(w, r, &in) {
		return
	}
	a, err := s.Eng.Approve(r.Context(), r.PathValue("id"), in.Approver)
	if errors.Is(err, engine.ErrWillRetry) {
		writeJSON(w, http.StatusAccepted, map[string]any{"action": a, "message": err.Error()})
		return
	}
	if a != nil && a.Status == "failed" {
		writeJSON(w, 200, map[string]any{"action": a, "message": "The action was refused safely; see the result for the reason."})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"action": a})
}

func (s *Server) reject(w http.ResponseWriter, r *http.Request) {
	var in struct{ Approver string }
	if !decode(w, r, &in) {
		return
	}
	if err := s.Eng.Reject(r.Context(), r.PathValue("id"), in.Approver); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "rejected"})
}

func (s *Server) inbound(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ItemID string `json:"item_id"`
		Text   string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	msg, ex, err := s.Eng.InboundReply(r.Context(), in.ItemID, in.Text)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"message": msg, "extraction": ex})
}

func (s *Server) reconcile(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Eng.Reconcile(r.Context()))
}

func (s *Server) advance(w http.ResponseWriter, r *http.Request) {
	var in struct{ Days int }
	if !decode(w, r, &in) {
		return
	}
	for i := 0; i < in.Days; i++ {
		if err := s.Eng.AdvanceFake(r.Context(), 1); err != nil {
			s.fail(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"advanced_days": in.Days, "note": "FAKE items only"})
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 500 {
		n = 200
	}
	es, err := store.ListAudit(r.Context(), s.Eng.Store.DB, r.URL.Query().Get("item"), n)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, es)
}

func (s *Server) auditVerify(w http.ResponseWriter, r *http.Request) {
	n, broken, err := store.VerifyAudit(r.Context(), s.Eng.Store.DB)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"entries": n, "intact": broken == 0, "first_broken_id": broken})
}

func (s *Server) benchRun(w http.ResponseWriter, r *http.Request) {
	if !s.benchOn.CompareAndSwap(false, true) {
		writeErr(w, http.StatusConflict, "a benchmark run is already in progress")
		return
	}
	go func() {
		defer s.benchOn.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		opt := bench.Options{DatabaseURL: s.Cfg.DatabaseURL}
		if s.Cfg.LiveConfigured() {
			opt.Live = s.Live
		}
		res, err := bench.Run(ctx, opt)
		if err != nil {
			s.log().Error("benchmark failed", "err", err)
			return
		}
		if err := store.SaveBench(ctx, s.Eng.Store.DB, res.JSON()); err != nil {
			s.log().Error("saving benchmark failed", "err", err)
		}
		s.Hub.Publish("bench", map[string]any{"done": true})
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (s *Server) benchLatest(w http.ResponseWriter, r *http.Request) {
	b, at, err := store.LatestBench(r.Context(), s.Eng.Store.DB)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, 200, map[string]any{"available": false, "running": s.benchOn.Load()})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"available": true, "running": s.benchOn.Load(), "saved_at": at, "result": json.RawMessage(b)})
}

// seed creates a repeatable demo data set. FAKE items work without credentials;
// the simulated and live-sandbox groups are added only when sandbox credentials exist.
func (s *Server) seed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var made []string
	if s.Cfg.FakeEnabled {
		var rec []engine.Recipient
		for _, d := range demo.Standard() {
			rec = append(rec, engine.Recipient{
				Name:        d.Name,
				Email:       d.Email,
				Amount:      d.Amount,
				Currency:    d.Currency,
				Purpose:     d.Purpose,
				FakeOutcome: d.FakeOutcome,
			})
		}
		if s.Eng.Fake != nil {
			s.Eng.Fake.Register("aisha.khan@example.com")
		}
		if _, _, err := s.Eng.CreateBatch(ctx, engine.CreateBatchInput{Mode: state.ModeFake, Recipients: rec}); err != nil && !errors.Is(err, engine.ErrWillRetry) {
			s.fail(w, err)
			return
		}
		made = append(made, "FAKE batch with 12 recipients")
	}
	if s.Cfg.LiveConfigured() {
		var sim []engine.Recipient
		for tag, name := range map[string]string{"ERRPYO059": "Sim Invalid Email", "ERRPYO044": "Sim Account Locked", "ERRPYO043": "Sim Currency"} {
			sim = append(sim, engine.Recipient{Name: name, Email: strings.ToLower(strings.ReplaceAll(name, " ", ".")) + "@example.com", Amount: "25.00", SimTag: tag})
		}
		if _, _, err := s.Eng.CreateBatch(ctx, engine.CreateBatchInput{Mode: state.ModeSim, Recipients: sim}); err == nil {
			made = append(made, "SIMULATED batch (PayPal test values)")
		} else {
			made = append(made, "SIMULATED batch failed: "+err.Error())
		}
		live := []engine.Recipient{
			{Name: "Live Unclaimed One", Email: fmt.Sprintf("nobody-registered-%d@example.com", time.Now().Unix()), Amount: "1.00"},
			{Name: "Live Unclaimed Two", Email: fmt.Sprintf("nobody-registered-%d-b@example.com", time.Now().Unix()), Amount: "1.00"},
		}
		if _, _, err := s.Eng.CreateBatch(ctx, engine.CreateBatchInput{Mode: state.ModeLive, Recipients: live}); err == nil {
			made = append(made, "LIVE_SANDBOX batch (2 unregistered recipients, expect UNCLAIMED)")
		} else {
			made = append(made, "LIVE_SANDBOX batch failed: "+err.Error())
		}
	}
	if len(made) == 0 {
		writeErr(w, http.StatusConflict, "enable FAKE_ENABLED or configure PayPal sandbox credentials")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"created": made})
}

// ---------- conflicts ----------

func (s *Server) listConflicts(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("status")
	list, err := store.ListConflicts(r.Context(), s.Eng.Store.DB, st, 200)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) getConflict(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := store.GetConflict(r.Context(), s.Eng.Store.DB, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) resolveConflict(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Resolution string `json:"resolution"`
		Approver   string `json:"approver"`
		Value      string `json:"value"`
		Note       string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Approver) == "" {
		writeErr(w, http.StatusBadRequest, "an approver name is required")
		return
	}
	if err := s.Eng.ResolveConflict(r.Context(), id, in.Approver, in.Resolution, in.Value, in.Note); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "resolved"})
}

func (s *Server) seedConflicts(w http.ResponseWriter, r *http.Request) {
	if err := s.Eng.SeedConflicts(r.Context()); err != nil {
		if errors.Is(err, engine.ErrAlreadySeeded) {
			writeErr(w, http.StatusConflict, "conflict demo pack already seeded")
			return
		}
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "seeded"})
}

// ---------- notes ----------

func (s *Server) createNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Author string `json:"author"`
		Body   string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Author) == "" {
		in.Author = "operator"
	}
	if strings.TrimSpace(in.Body) == "" {
		writeErr(w, http.StatusBadRequest, "note body cannot be empty")
		return
	}
	if len(in.Body) > 2000 || len(in.Author) > 80 {
		writeErr(w, http.StatusBadRequest, "note is too long (max 2000 characters, author max 80)")
		return
	}
	n := &store.Note{
		ItemID: id,
		Author: strings.TrimSpace(in.Author),
		Body:   strings.TrimSpace(in.Body),
	}
	ctx := r.Context()
	if _, err := store.GetItem(ctx, s.Eng.Store.DB, id, false); err != nil {
		s.fail(w, err)
		return
	}
	if err := store.InsertNote(ctx, s.Eng.Store.DB, n); err != nil {
		s.fail(w, err)
		return
	}
	_ = s.Eng.Store.Tx(ctx, func(tx *sql.Tx) error {
		return store.AppendAudit(ctx, tx, in.Author, "operator_note_added", id, map[string]any{"note_id": n.ID})
	})
	s.Hub.Publish("note", map[string]any{"item": id, "note_id": n.ID})
	writeJSON(w, http.StatusCreated, n)
}

// ---------- snooze & bulk acknowledge ----------

func (s *Server) snoozeAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Days int `json:"days"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Days != 1 && in.Days != 3 && in.Days != 7 {
		writeErr(w, http.StatusBadRequest, "days must be 1, 3, or 7")
		return
	}
	ctx := r.Context()
	a, err := store.GetAction(ctx, s.Eng.Store.DB, id, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	if a.Status != "proposed" {
		writeErr(w, http.StatusConflict, "only a proposed action can be snoozed")
		return
	}
	err = s.Eng.Store.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE actions SET snoozed_until = now() + make_interval(days => $2) WHERE id=$1`, id, in.Days); err != nil {
			return err
		}
		return store.AppendAudit(ctx, tx, "operator", "action_snoozed", a.ItemID, map[string]any{"action_id": id, "days": in.Days})
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Hub.Publish("action", map[string]any{"id": id})
	writeJSON(w, 200, map[string]string{"status": "snoozed"})
}

func (s *Server) bulkAcknowledge(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ActionIDs []string `json:"action_ids"`
		Approver  string   `json:"approver"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Approver) == "" {
		writeErr(w, http.StatusBadRequest, "approver name is required")
		return
	}
	if len(in.ActionIDs) > 200 {
		writeErr(w, http.StatusBadRequest, "at most 200 actions at a time")
		return
	}
	ctx := r.Context()
	for _, aid := range in.ActionIDs {
		a, err := store.GetAction(ctx, s.Eng.Store.DB, aid, false)
		if err != nil {
			continue
		}
		// Invariant: Money-moving actions can NEVER be bulk-approved!
		if rules.Kind(a.Kind).MoneyMoving() {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("action %s is money-moving (%s) and cannot be bulk acknowledged", a.ID, a.Kind))
			return
		}
	}
	var count int
	for _, aid := range in.ActionIDs {
		if _, err := s.Eng.Approve(ctx, aid, in.Approver); err == nil {
			count++
		}
	}
	writeJSON(w, 200, map[string]any{"acknowledged": count})
}

// ---------- audit export CSV ----------

// csvSafe stops spreadsheet programs from running a cell that starts with = + - or @ as a formula.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

func (s *Server) exportAuditCSV(w http.ResponseWriter, r *http.Request) {
	entries, err := store.ListAudit(r.Context(), s.Eng.Store.DB, "", 5000)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=\"audit_log.csv\"")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "timestamp", "actor", "event", "item_id", "prev_hash", "hash", "details"})
	for _, e := range entries {
		_ = cw.Write([]string{
			strconv.FormatInt(e.ID, 10),
			e.TS.Format(time.RFC3339),
			csvSafe(e.Actor),
			csvSafe(e.Event),
			e.ItemID,
			e.PrevHash,
			e.Hash,
			csvSafe(string(e.Details)),
		})
	}
	cw.Flush()
}

// ---------- reference ----------

func (s *Server) reference(w http.ResponseWriter, r *http.Request) {
	statuses := []string{
		string(state.Created), string(state.Pending), string(state.Success),
		string(state.Unclaimed), string(state.Failed), string(state.OnHold),
		string(state.Blocked), string(state.Returned), string(state.Refunded),
		string(state.Reversed), string(state.Canceled), string(state.Unknown),
	}
	actionKinds := []string{
		string(rules.KindNotify), string(rules.KindCancel), string(rules.KindReissue),
		string(rules.KindEscalate), string(rules.KindFlag),
	}
	writeJSON(w, 200, map[string]any{
		"routes":               s.routes,
		"statuses":             statuses,
		"action_kinds":         actionKinds,
		"template_keys":        ai.TemplateKeys(),
		"sim_tags":             engine.SimTags,
		"conflict_resolutions": engine.AllowedResolutions,
		"rules_matrix":         rules.Matrix(),
	})
}

func (s *Server) demoPayouts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"standard": demo.Standard(), "conflicts": demo.Conflicts()})
}

// checkDuplicates warns before a batch is created when it repeats a recent obligation.
func (s *Server) checkDuplicates(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Recipients []struct {
			Email    string `json:"email"`
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"recipients"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Recipients) > 500 {
		writeErr(w, http.StatusBadRequest, "too many recipients")
		return
	}
	type dup struct {
		Index     int    `json:"index"`
		Email     string `json:"email"`
		Amount    string `json:"amount"`
		Currency  string `json:"currency"`
		ItemID    string `json:"item_id"`
		CreatedAt string `json:"created_at"`
	}
	out := []dup{}
	since := time.Now().Add(-7 * 24 * time.Hour)
	for i, rec := range in.Recipients {
		cur := rec.Currency
		if cur == "" {
			cur = "USD"
		}
		if !ai.ValidEmail(rec.Email) {
			continue
		}
		found, err := store.RecentDuplicates(r.Context(), s.Eng.Store.DB, rec.Email, rec.Amount, cur, since)
		if err != nil {
			continue // a malformed amount simply produces no match; validation happens on create
		}
		if len(found) > 0 {
			out = append(out, dup{Index: i, Email: rec.Email, Amount: rec.Amount, Currency: cur, ItemID: found[0].ID, CreatedAt: found[0].CreatedAt.Format(time.RFC3339)})
		}
	}
	writeJSON(w, 200, map[string]any{"duplicates": out})
}
