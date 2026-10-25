package bench

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mandar-1dev/payoutrescue/internal/ai"
	"github.com/mandar-1dev/payoutrescue/internal/engine"
	"github.com/mandar-1dev/payoutrescue/internal/fakepaypal"
	"github.com/mandar-1dev/payoutrescue/internal/paypal"
	"github.com/mandar-1dev/payoutrescue/internal/state"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

func paidOnceCount(f *fakepaypal.Fake) int {
	count := map[string]int{}
	for _, p := range f.Payments() {
		count[fakepaypal.LogicalKey(p.SenderItemID)]++
	}
	n := 0
	for _, c := range count {
		if c == 1 {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------- PayoutRescue

type prH struct {
	ctx      context.Context
	st       *store.Store
	f        *fakepaypal.Fake
	e        *engine.Engine
	logicals map[string]string // key -> logical payout id
	steps    int
	checks   []func() bool
}

func newPRHarness(ctx context.Context, st *store.Store) *prH {
	_, _ = st.DB.ExecContext(ctx, `TRUNCATE bench_runs, messages, actions, webhook_events, audit_log, payout_items, payout_batches, logical_payouts RESTART IDENTITY CASCADE`)
	f := fakepaypal.New()
	return &prH{ctx: ctx, st: st, f: f, logicals: map[string]string{},
		e: &engine.Engine{Store: st, Fake: f, Platform: "Bench", WebhookBypass: true, ResumeAfter: time.Nanosecond}}
}

func (h *prH) Close() {}

func (h *prH) create(email, outcome string) string {
	r := engine.Recipient{Name: "Person", Email: email, Amount: "10.00", Currency: "USD", FakeOutcome: outcome}
	_, items, _ := h.e.CreateBatch(h.ctx, engine.CreateBatchInput{Mode: state.ModeFake, Recipients: []engine.Recipient{r}})
	if len(items) == 0 {
		return ""
	}
	key := fakepaypal.LogicalKey(items[0].SenderItemID)
	h.logicals[key] = items[0].LogicalPayoutID
	return key
}

func (h *prH) Create(email string) string { return h.create(email, "") }
func (h *prH) CreateScripted(email, status, errName string) string {
	o := status
	if status == "FAILED" {
		o = "FAILED:" + errName
	}
	return h.create(email, o)
}

func (h *prH) Advance(days int) {
	for i := 0; i < days; i++ {
		_ = h.e.AdvanceFake(h.ctx, 1)
	}
}

func (h *prH) Claim(key string) { h.f.ClaimAll(key) }

func (h *prH) latest(key string) *store.Item {
	var id string
	err := h.st.DB.QueryRowContext(h.ctx, `SELECT id FROM payout_items WHERE logical_payout_id=$1 ORDER BY attempt DESC LIMIT 1`, h.logicals[key]).Scan(&id)
	if err != nil {
		return nil
	}
	it, _ := store.GetItem(h.ctx, h.st.DB, id, false)
	return it
}

func (h *prH) Correct(key, email string, register bool) {
	if register {
		h.f.Register(email)
	}
	if it := h.latest(key); it != nil {
		if err := h.e.SetCorrectedEmail(h.ctx, it.ID, email, "operator"); err == nil {
			h.steps++
		}
	}
}

func (h *prH) Reply(key, text string) {
	if it := h.latest(key); it != nil {
		_, _, _ = h.e.InboundReply(h.ctx, it.ID, text)
	}
}

// Operate runs the reconciler and approves every proposed cancel or re-issue,
// like an operator who approves what the system proposes.
func (h *prH) Operate() {
	for round := 0; round < 6; round++ {
		h.e.Reconcile(h.ctx)
		as, _ := store.ListActions(h.ctx, h.st.DB, "proposed", "", 200)
		n := 0
		for _, a := range as {
			if a.Kind == "cancel" || a.Kind == "reissue" {
				_, _ = h.e.Approve(h.ctx, a.ID, "bench-operator")
				h.steps++
				n++
			}
		}
		if n == 0 {
			// one more pass lets resumable work (lost responses) finish
			h.e.Reconcile(h.ctx)
			return
		}
	}
}

func (h *prH) Script(email, status, errName string) { h.f.SetOutcome(email, status, errName) }

// Flip makes the fake report another status and reads the payout back, like a webhook would.
func (h *prH) Flip(key, status string) {
	if it := h.latest(key); it != nil {
		h.f.SetStatus(it.PayPalItemID, status, "")
		_ = h.e.ProcessItem(h.ctx, it.ID)
	}
}

func (h *prH) DropNextCreate() { h.f.DropResponse = 1 }
func (h *prH) FailNextCreate() { h.f.FailCreate = 1 }

func (h *prH) Chaos(kind, key string) {
	it := h.latest(key)
	if it == nil {
		h.checks = append(h.checks, func() bool { return false })
		return
	}
	n := len(h.checks) + 1
	event := func(id, itemID string) []byte {
		return []byte(fmt.Sprintf(`{"id":%q,"event_type":"PAYMENT.PAYOUTS-ITEM.UNCLAIMED","resource":{"payout_item_id":%q}}`, id, itemID))
	}
	switch kind {
	case "duplicate":
		body := event(fmt.Sprintf("WH-dup-%d", n), it.PayPalItemID)
		id, dup1, err1 := h.e.IngestWebhook(h.ctx, nil, body)
		_, dup2, _ := h.e.IngestWebhook(h.ctx, nil, body)
		_ = h.e.ProcessEvent(h.ctx, id)
		h.checks = append(h.checks, func() bool { return err1 == nil && !dup1 && dup2 })
	case "stale":
		h.f.Claim(it.PayPalItemID)
		id, _, _ := h.e.IngestWebhook(h.ctx, nil, event("WH-stale", it.PayPalItemID)) // payload still says UNCLAIMED
		_ = h.e.ProcessEvent(h.ctx, id)
		h.checks = append(h.checks, func() bool { c := h.latest(key); return c != nil && c.Status == state.Success })
	case "missing":
		h.f.Claim(it.PayPalItemID) // no webhook is ever sent
		h.checks = append(h.checks, func() bool { c := h.latest(key); return c != nil && c.Status == state.Success })
	case "delayed":
		id, _, _ := h.e.IngestWebhook(h.ctx, nil, event("WH-delayed", it.PayPalItemID)) // stored, not processed
		h.checks = append(h.checks, func() bool {
			ev, err := store.GetEvent(h.ctx, h.st.DB, id)
			return err == nil && ev.Processed
		})
	case "unknown":
		id, _, _ := h.e.IngestWebhook(h.ctx, nil, event("WH-unknown", "DOES-NOT-EXIST"))
		_ = h.e.ProcessEvent(h.ctx, id)
		h.checks = append(h.checks, func() bool {
			ev, err := store.GetEvent(h.ctx, h.st.DB, id)
			return err == nil && ev.Processed
		})
	case "badsig":
		h.e.WebhookBypass = false
		_, _, err := h.e.IngestWebhook(h.ctx, nil, event("WH-bad", it.PayPalItemID))
		h.e.WebhookBypass = true
		h.checks = append(h.checks, func() bool { return err == engine.ErrBadSignature })
	}
}

func (h *prH) count(q string, args ...any) int {
	var n int
	_ = h.st.DB.QueryRowContext(h.ctx, q, args...).Scan(&n)
	return n
}

func (h *prH) Outcome() Outcome {
	ok := true
	for _, c := range h.checks {
		if !c() {
			ok = false
		}
	}
	var kinds []string
	if rows, err := h.st.DB.QueryContext(h.ctx, `SELECT kind FROM conflicts WHERE status='open' ORDER BY kind`); err == nil {
		for rows.Next() {
			var k string
			_ = rows.Scan(&k)
			kinds = append(kinds, k)
		}
		rows.Close()
	}
	return Outcome{
		DuplicatePayment: len(h.f.Duplicates()),
		Paid:             paidOnceCount(h.f),
		MaxAttempt:       h.count(`SELECT coalesce(max(attempt),0) FROM payout_items`),
		Conflicts:        kinds,
		UnsafeRetries: h.count(`SELECT count(*) FROM payout_items i JOIN payout_items o ON o.id = i.original_item_id
			WHERE o.status IN ('ONHOLD','BLOCKED') OR o.error_code IN ('RECEIVER_COUNTRY_NOT_ALLOWED','REGULATORY_BLOCKED','RISK_DECLINE','DUPLICATE_ITEM','CURRENCY_NOT_SUPPORTED_FOR_RECEIVER')`),
		UnnoticedReturns: h.count(`SELECT count(*) FROM payout_items i WHERE i.status='RETURNED' AND NOT EXISTS
			(SELECT 1 FROM actions a WHERE a.item_id=i.id AND (a.kind='cancel' OR a.template_key IN ('UNCLAIMED_REMINDER_D20','UNCLAIMED_FINAL_D27')))`),
		ManualSteps: h.steps,
		Ok:          ok,
	}
}

// ---------------------------------------------------------------- naive retry script

type naiveLogical struct {
	email     string
	sends     int
	lastSend  int
	items     []string
	exception bool
}

// naive models a typical script: retry anything that is not SUCCESS, with fresh ids.
type naive struct {
	f      *fakepaypal.Fake
	ctx    context.Context
	day    int
	reqSeq int
	n      int
	lg     map[string]*naiveLogical
	unsafe int
}

func newNaive() *naive {
	return &naive{f: fakepaypal.New(), ctx: context.Background(), lg: map[string]*naiveLogical{}}
}

func (n *naive) Close() {}

var nonRetryable = map[string]bool{"RECEIVER_COUNTRY_NOT_ALLOWED": true, "REGULATORY_BLOCKED": true, "RISK_DECLINE": true, "DUPLICATE_ITEM": true, "CURRENCY_NOT_SUPPORTED_FOR_RECEIVER": true}

func (n *naive) send(key string) {
	lg := n.lg[key]
	lg.sends++
	lg.lastSend = n.day
	for attempt := 0; attempt < 2; attempt++ { // retry on error with FRESH ids, as simple scripts do
		n.reqSeq++
		sid := fmt.Sprintf("naive-%s-%d%d", key, lg.sends, attempt)
		req := paypal.CreateBatchRequest{SenderBatchID: "naivebatch-" + sid, Items: []paypal.CreateItem{{SenderItemID: sid, Receiver: lg.email, Currency: "USD", Value: "10.00"}}}
		res, err := n.f.CreateBatch(n.ctx, req, fmt.Sprintf("naive-req-%d", n.reqSeq))
		if err != nil {
			continue
		}
		if b, gerr := n.f.GetBatch(n.ctx, res.BatchID); gerr == nil {
			for _, it := range b.Items {
				lg.items = append(lg.items, it.PayoutItemID)
			}
		}
		return
	}
}

func (n *naive) Create(email string) string {
	n.n++
	key := fmt.Sprintf("n%d", n.n)
	n.lg[key] = &naiveLogical{email: email}
	n.send(key)
	return key
}

func (n *naive) CreateScripted(email, status, errName string) string {
	n.f.SetOutcome(email, status, errName)
	return n.Create(email)
}

func (n *naive) status(id string) (string, string) {
	it, err := n.f.GetItem(n.ctx, id)
	if err != nil {
		return "", ""
	}
	return it.TransactionStatus, it.ErrorName
}

func (n *naive) hasSuccess(key string) bool {
	for _, id := range n.lg[key].items {
		if s, _ := n.status(id); s == "SUCCESS" {
			return true
		}
	}
	return false
}

func (n *naive) daily() {
	for key, lg := range n.lg {
		if n.hasSuccess(key) || len(lg.items) == 0 {
			continue
		}
		s, code := n.status(lg.items[len(lg.items)-1])
		switch s {
		case "FAILED", "RETURNED", "CANCELED":
			lg.exception = true
			if lg.sends < 5 {
				if s == "FAILED" && (nonRetryable[code] || !knownCode(code)) {
					n.unsafe++
				}
				n.send(key)
			}
		case "UNCLAIMED":
			lg.exception = true
			if n.day-lg.lastSend >= 27 && lg.sends < 5 {
				n.send(key)
			}
		case "ONHOLD", "BLOCKED":
			lg.exception = true
			if n.day-lg.lastSend >= 14 && lg.sends < 5 {
				n.unsafe++
				n.send(key)
			}
		}
	}
}

func knownCode(c string) bool {
	switch c {
	case "INVALID_EMAIL", "RECEIVER_UNREGISTERED", "RECEIVER_UNCONFIRMED", "RECEIVER_ACCOUNT_LOCKED", "RECEIVER_ACCOUNT_LIMITATION", "RECEIVING_LIMIT_EXCEEDED":
		return true
	}
	return nonRetryable[c]
}

func (n *naive) Advance(days int) {
	for i := 0; i < days; i++ {
		n.f.Advance(1)
		n.day++
		n.daily()
	}
}

func (n *naive) Claim(key string) { n.f.ClaimAll(key) }

func (n *naive) Correct(key, email string, register bool) {
	if register {
		n.f.Register(email)
	}
	lg := n.lg[key]
	lg.email, lg.exception = email, true
	if !n.hasSuccess(key) { // the operator fixes the email and re-runs the script
		n.send(key)
	}
}

var emailFind = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

func (n *naive) Reply(key, text string) {
	if len(ai.DetectInjection(text)) > 0 { // a human reading the reply would spot the obvious attacks
		return
	}
	if m := emailFind.FindAllString(text, -1); len(m) == 1 {
		n.Correct(key, strings.ToLower(m[0]), false)
	}
}

func (n *naive) Operate()               {}
func (n *naive) DropNextCreate()        { n.f.DropResponse = 1 }
func (n *naive) FailNextCreate()        { n.f.FailCreate = 1 }
func (n *naive) Chaos(kind, key string) {}

func (n *naive) Outcome() Outcome {
	o := Outcome{DuplicatePayment: len(n.f.Duplicates()), Paid: paidOnceCount(n.f), UnsafeRetries: n.unsafe}
	for key, lg := range n.lg {
		for _, id := range lg.items {
			if s, _ := n.status(id); s == "RETURNED" {
				o.UnnoticedReturns++ // the script never notices a return
			}
		}
		if lg.exception {
			o.ManualSteps += AssumedManualStepsPerException
		}
		_ = key
	}
	o.Ok = true
	return o
}

func (n *naive) Script(email, status, errName string) { n.f.SetOutcome(email, status, errName) }
func (n *naive) Flip(key, status string)              {}
