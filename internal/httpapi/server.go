// Package httpapi serves the JSON API, the PayPal webhook, SSE and the embedded SPA.
package httpapi

import (
	"context"
	"crypto/subtle"
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
	"github.com/mandar-1dev/payoutrescue/internal/engine"
	"github.com/mandar-1dev/payoutrescue/internal/paypal"
	"github.com/mandar-1dev/payoutrescue/internal/rules"
	"github.com/mandar-1dev/payoutrescue/internal/sse"
	"github.com/mandar-1dev/payoutrescue/internal/state"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

type Server struct {
	Eng     *engine.Engine
	Cfg     config.Config
	Hub     *sse.Hub
	Static  fs.FS
	Live    paypal.Client
	Log     *slog.Logger
	benchOn atomic.Bool
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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := s.Eng.Store.DB.PingContext(ctx); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "database not ready")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /api/public", s.public)
	mux.HandleFunc("POST /api/webhooks/paypal", s.webhook)

	mux.HandleFunc("GET /api/session", s.authed(s.session))
	mux.HandleFunc("GET /api/limbo", s.authed(s.limbo))
	mux.HandleFunc("POST /api/batches", s.authed(s.createBatch))
	mux.HandleFunc("GET /api/batches", s.authed(s.listBatches))
	mux.HandleFunc("GET /api/batches/{id}", s.authed(s.getBatch))
	mux.HandleFunc("GET /api/items", s.authed(s.listItems))
	mux.HandleFunc("GET /api/items/{id}", s.authed(s.getItem))
	mux.HandleFunc("POST /api/items/{id}/refresh", s.authed(s.refreshItem))
	mux.HandleFunc("POST /api/items/{id}/corrected-email", s.authed(s.correctedEmail))
	mux.HandleFunc("GET /api/actions", s.authed(s.listActions))
	mux.HandleFunc("POST /api/actions/{id}/approve", s.authed(s.approve))
	mux.HandleFunc("POST /api/actions/{id}/reject", s.authed(s.reject))
	mux.HandleFunc("POST /api/messages/inbound", s.authed(s.inbound))
	mux.HandleFunc("POST /api/admin/reconcile", s.authed(s.reconcile))
	mux.HandleFunc("POST /api/admin/advance", s.authed(s.advance))
	mux.HandleFunc("POST /api/demo/seed", s.authed(s.seed))
	mux.HandleFunc("GET /api/audit", s.authed(s.audit))
	mux.HandleFunc("GET /api/audit/verify", s.authed(s.auditVerify))
	mux.HandleFunc("POST /api/bench/run", s.authed(s.benchRun))
	mux.HandleFunc("GET /api/bench/latest", s.authed(s.benchLatest))
	mux.HandleFunc("GET /api/events", s.authed(s.Hub.ServeHTTP))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, http.StatusNotFound, "unknown API route") })
	mux.HandleFunc("/", s.spa)
	return s.middleware(mux)
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
	writeJSON(w, 200, map[string]any{"item": it, "actions": actions, "messages": msgs, "audit": aud, "preflight": pre, "attempts": history})
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
		rec := []engine.Recipient{
			{Name: "Ben Ortiz", Email: "ben.ortiz@example.com", Amount: "120.00", Purpose: "October creator payout"},
			{Name: "Chen Wei", Email: "chen.wei@example.com", Amount: "85.50", Purpose: "October creator payout"},
			{Name: "Dana Silva", Email: "dana.silva", Amount: "60.00", Purpose: "Affiliate commission"},
			{Name: "Eli Novak", Email: "eli.novak@example.com", Amount: "42.00", Purpose: "Affiliate commission", FakeOutcome: "FAILED:RECEIVER_ACCOUNT_LOCKED"},
			{Name: "Farah Ali", Email: "farah.ali@example.com", Amount: "75.00", Purpose: "Freelance invoice", FakeOutcome: "FAILED:CURRENCY_NOT_SUPPORTED_FOR_RECEIVER"},
			{Name: "Gus Berg", Email: "gus.berg@example.com", Amount: "30.00", Purpose: "Freelance invoice", FakeOutcome: "FAILED:RECEIVER_COUNTRY_NOT_ALLOWED"},
			{Name: "Hana Sato", Email: "hana.sato@example.com", Amount: "55.00", Purpose: "Affiliate commission", FakeOutcome: "FAILED:RECEIVING_LIMIT_EXCEEDED"},
			{Name: "Ivan Petrov", Email: "ivan.petrov@example.com", Amount: "210.00", Purpose: "Creator bonus", FakeOutcome: "ONHOLD"},
			{Name: "Jade Moore", Email: "jade.moore@example.com", Amount: "99.00", Purpose: "Creator bonus", FakeOutcome: "BLOCKED"},
			{Name: "Kofi Mensah", Email: "kofi.mensah@example.com", Amount: "150.00", Purpose: "October creator payout"},
			{Name: "Lena Fox", Email: "lena.fox@example.com", Amount: "65.00", Purpose: "Affiliate commission"},
			{Name: "Aisha Khan", Email: "aisha.khan@example.com", Amount: "300.00", Purpose: "Creator payout"},
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
