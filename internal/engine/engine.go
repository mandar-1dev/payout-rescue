// Package engine orchestrates payout tracking: it reads state back from PayPal,
// applies it through the state machine, proposes actions with the rules, and
// executes only approved actions with the never-pay-twice invariants.
package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mandar-1dev/payoutrescue/internal/ai"
	"github.com/mandar-1dev/payoutrescue/internal/fakepaypal"
	"github.com/mandar-1dev/payoutrescue/internal/paypal"
	"github.com/mandar-1dev/payoutrescue/internal/rules"
	"github.com/mandar-1dev/payoutrescue/internal/state"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

var (
	ErrNoClient      = errors.New("PayPal sandbox credentials are not configured for this mode")
	ErrInconclusive  = errors.New("PayPal readback was inconclusive; nothing was changed")
	ErrWillRetry     = errors.New("approved; PayPal was unreachable, the same request will be retried safely")
	ErrBadSignature  = errors.New("webhook signature could not be verified")
	ErrNotApprovable = errors.New("action is not awaiting approval")
)

type Engine struct {
	Store         *store.Store
	Live          paypal.Client    // nil when no sandbox credentials
	Fake          *fakepaypal.Fake // nil when disabled
	AI            ai.Provider
	Platform      string
	ReplyTo       string
	WebhookBypass bool
	WebhookID     string
	Log           *slog.Logger
	Notify        func(event string, data any)
	// ResumeAfter is how old an unfinished create/approval must be before the
	// reconciler retries it (default 45s; the benchmark sets a tiny value).
	ResumeAfter time.Duration
}

func (e *Engine) resumeAfter() time.Duration {
	if e.ResumeAfter > 0 {
		return e.ResumeAfter
	}
	return 45 * time.Second
}

func (e *Engine) log() *slog.Logger {
	if e.Log == nil {
		return slog.Default()
	}
	return e.Log
}

func (e *Engine) emit(event string, data any) {
	if e.Notify != nil {
		e.Notify(event, data)
	}
}

// client returns the PayPal client for a mode. SIMULATED uses the live sandbox
// client because PayPal itself answers with canned test-value responses.
func (e *Engine) client(mode string) (paypal.Client, error) {
	switch mode {
	case state.ModeFake:
		if e.Fake == nil {
			return nil, errors.New("fake PayPal is disabled")
		}
		return e.Fake, nil
	case state.ModeLive, state.ModeSim:
		if e.Live == nil {
			return nil, ErrNoClient
		}
		return e.Live, nil
	}
	return nil, fmt.Errorf("unknown mode %q", mode)
}

// SimTags maps the supported PayPal test values to the error each one simulates.
var SimTags = map[string]string{
	"ERRPYO047": "RECEIVER_UNREGISTERED",
	"ERRPYO046": "RECEIVER_UNCONFIRMED",
	"ERRPYO044": "RECEIVER_ACCOUNT_LOCKED",
	"ERRPYO059": "INVALID_EMAIL",
	"ERRPYO043": "CURRENCY_NOT_SUPPORTED_FOR_RECEIVER",
	"ERRPYO045": "RECEIVER_COUNTRY_NOT_ALLOWED",
	"ERRPYO049": "RECEIVING_LIMIT_EXCEEDED",
	"ERRPYO061": "RECEIVER_ACCOUNT_LIMITATION",
}

const (
	simCreateNote  = "POSPYO001" // canned 201 for create
	simCancelOK    = "POSPOI002" // canned success for cancel
	simItemSuccess = "POSPOI001" // canned 200 item payload
)

func (e *Engine) readItem(ctx context.Context, it *store.Item) (paypal.ItemInfo, error) {
	c, err := e.client(it.Mode)
	if err != nil {
		return paypal.ItemInfo{}, err
	}
	if it.Mode == state.ModeSim {
		if it.SimTag == "" {
			return paypal.ItemInfo{}, errors.New("simulated item has no test value")
		}
		return paypal.SimItem(ctx, c, it.SimTag)
	}
	if it.PayPalItemID == "" {
		return paypal.ItemInfo{}, ErrInconclusive
	}
	return c.GetItem(ctx, it.PayPalItemID)
}

func newID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func short(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > 8 {
		s = s[:8]
	}
	return s
}

// ---------- creating batches ----------

type Recipient struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Purpose  string `json:"purpose"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	SimTag   string `json:"sim_tag"`
	// FakeOutcome optionally scripts a FAKE item: ONHOLD, BLOCKED, UNCLAIMED, FAILED:<ERR>.
	FakeOutcome string `json:"fake_outcome"`
}

type CreateBatchInput struct {
	Mode       string      `json:"mode"`
	Recipients []Recipient `json:"recipients"`
}

var amountRe = regexp.MustCompile(`^\d{1,9}(\.\d{1,2})?$`)
var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

func validateInput(in *CreateBatchInput) error {
	if in.Mode == "" {
		in.Mode = state.ModeFake
	}
	if in.Mode != state.ModeFake && in.Mode != state.ModeLive && in.Mode != state.ModeSim {
		return fmt.Errorf("mode must be %s, %s or %s", state.ModeLive, state.ModeSim, state.ModeFake)
	}
	if n := len(in.Recipients); n < 1 || n > 100 {
		return errors.New("a batch needs between 1 and 100 recipients")
	}
	for i := range in.Recipients {
		r := &in.Recipients[i]
		r.Name, r.Email = strings.TrimSpace(r.Name), strings.TrimSpace(r.Email)
		r.Currency = strings.ToUpper(strings.TrimSpace(r.Currency))
		if r.Currency == "" {
			r.Currency = "USD"
		}
		if r.Name == "" || len(r.Name) > 120 {
			return fmt.Errorf("recipient %d: name is required (max 120 characters)", i+1)
		}
		if r.Email == "" || len(r.Email) > 254 {
			return fmt.Errorf("recipient %d: email is required", i+1)
		}
		if !amountRe.MatchString(r.Amount) || r.Amount == "0" || strings.Trim(r.Amount, "0.") == "" {
			return fmt.Errorf("recipient %d: amount must be a positive number with up to 2 decimals", i+1)
		}
		if !currencyRe.MatchString(r.Currency) {
			return fmt.Errorf("recipient %d: currency must be a 3-letter code", i+1)
		}
		if in.Mode == state.ModeLive && !ai.ValidEmail(r.Email) {
			return fmt.Errorf("recipient %d: live sandbox batches need a valid email (use FAKE or SIMULATED mode to test invalid emails)", i+1)
		}
		if in.Mode == state.ModeSim {
			if _, ok := SimTags[r.SimTag]; !ok {
				return fmt.Errorf("recipient %d: sim_tag must be one of the supported PayPal test values", i+1)
			}
		}
	}
	return nil
}

// CreateBatch records the batch and its items, then asks PayPal to send it.
// Items are stored first with deterministic ids so a crash or a lost response
// can be retried with the same ids and never creates a second payout.
func (e *Engine) CreateBatch(ctx context.Context, in CreateBatchInput) (*store.Batch, []store.Item, error) {
	if err := validateInput(&in); err != nil {
		return nil, nil, err
	}
	client, err := e.client(in.Mode)
	if err != nil {
		return nil, nil, err
	}
	senderBatch := "prb-" + newID(6)
	batch := &store.Batch{SenderBatchID: senderBatch, RequestID: "req-" + senderBatch, Mode: in.Mode}
	var items []store.Item
	err = e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.InsertBatch(ctx, tx, batch); err != nil {
			return err
		}
		for _, r := range in.Recipients {
			l := &store.Logical{Purpose: r.Purpose, RecipientName: r.Name, Amount: r.Amount, Currency: r.Currency}
			if err := store.InsertLogical(ctx, tx, l); err != nil {
				return err
			}
			it := store.Item{LogicalPayoutID: l.ID, BatchID: batch.ID, SenderItemID: fmt.Sprintf("pr-%s-a1", short(l.ID)),
				RecipientEmail: r.Email, Attempt: 1, Status: state.Created, Mode: in.Mode, SimTag: r.SimTag}
			if err := store.InsertItem(ctx, tx, &it, time.Now()); err != nil {
				return err
			}
			items = append(items, it)
		}
		return store.AppendAudit(ctx, tx, "system", "batch_created", "", map[string]any{"batch": batch.ID, "mode": in.Mode, "items": len(items), "sender_batch_id": senderBatch})
	})
	if err != nil {
		return nil, nil, err
	}
	// Scripted outcomes for the fake only.
	if in.Mode == state.ModeFake && e.Fake != nil {
		for _, r := range in.Recipients {
			switch {
			case r.FakeOutcome == "":
			case strings.HasPrefix(r.FakeOutcome, "FAILED:"):
				e.Fake.SetOutcome(r.Email, "FAILED", strings.TrimPrefix(r.FakeOutcome, "FAILED:"))
			default:
				e.Fake.SetOutcome(r.Email, strings.ToUpper(r.FakeOutcome), "")
			}
		}
	}
	if err := e.sendBatch(ctx, client, batch, items, in.Recipients); err != nil {
		return batch, items, err
	}
	return batch, items, nil
}

func (e *Engine) sendBatch(ctx context.Context, c paypal.Client, b *store.Batch, items []store.Item, rec []Recipient) error {
	req := paypal.CreateBatchRequest{SenderBatchID: b.SenderBatchID, EmailSubject: "You have a payout"}
	for i, it := range items {
		note := ""
		if b.Mode == state.ModeSim && i == 0 {
			note = simCreateNote
		}
		cur, amt := it.Currency, it.Amount
		if cur == "" && rec != nil && i < len(rec) {
			cur, amt = rec[i].Currency, rec[i].Amount
		}
		if cur == "" { // resumed batch: load from logical payout
			l, err := store.GetLogical(ctx, e.Store.DB, it.LogicalPayoutID)
			if err != nil {
				return err
			}
			cur, amt = l.Currency, l.Amount
		}
		req.Items = append(req.Items, paypal.CreateItem{SenderItemID: it.SenderItemID, Receiver: it.RecipientEmail, Currency: cur, Value: amt, Note: note})
	}
	res, err := c.CreateBatch(ctx, req, b.RequestID)
	if err != nil {
		var ae *paypal.APIError
		if errors.As(err, &ae) && !ae.Retryable() && b.Mode != state.ModeSim {
			// PayPal rejected the batch outright: nothing was created.
			_ = e.failBatch(ctx, b, items, ae.Name)
			return err
		}
		if b.Mode == state.ModeSim {
			return err
		}
		e.log().Warn("create batch unconfirmed; will retry with the same ids", "batch", b.ID, "err", err)
		return ErrWillRetry
	}
	return e.afterCreate(ctx, b, items, res)
}

func (e *Engine) failBatch(ctx context.Context, b *store.Batch, items []store.Item, code string) error {
	return e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.UpdateBatch(ctx, tx, b.ID, "", "FAILED"); err != nil {
			return err
		}
		for _, it := range items {
			if err := store.UpdateItemStatus(ctx, tx, it.ID, state.Failed, code); err != nil {
				return err
			}
		}
		return store.AppendAudit(ctx, tx, "paypal", "batch_rejected", "", map[string]any{"batch": b.ID, "error": code})
	})
}

func (e *Engine) afterCreate(ctx context.Context, b *store.Batch, items []store.Item, res paypal.BatchResult) error {
	ppBatch := res.BatchID
	if b.Mode == state.ModeSim {
		if ppBatch == "" {
			ppBatch = "SIM-" + short(b.ID)
		}
		err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
			if err := store.UpdateBatch(ctx, tx, b.ID, ppBatch, "SIMULATED"); err != nil {
				return err
			}
			for _, it := range items {
				if err := store.SetItemPayPalIDs(ctx, tx, it.ID, "SIM-"+newID(6), state.Pending, ""); err != nil {
					return err
				}
			}
			return store.AppendAudit(ctx, tx, "system", "batch_simulated", "", map[string]any{"batch": b.ID, "note": "PayPal test values return canned responses"})
		})
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := e.ProcessItem(ctx, it.ID); err != nil {
				e.log().Warn("sim readback failed", "item", it.ID, "err", err)
			}
		}
		return nil
	}
	if err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.UpdateBatch(ctx, tx, b.ID, ppBatch, "SENT"); err != nil {
			return err
		}
		return store.AppendAudit(ctx, tx, "paypal", "batch_sent", "", map[string]any{"batch": b.ID, "paypal_batch_id": ppBatch})
	}); err != nil {
		return err
	}
	b.PayPalBatchID = ppBatch
	return e.syncBatch(ctx, b)
}

// ---------- applying readbacks ----------

// applyInfo records a fresh PayPal readback for an item through the state machine.
func (e *Engine) applyInfo(ctx context.Context, itemID string, info paypal.ItemInfo, actor string) (changed bool, err error) {
	if strings.TrimSpace(info.TransactionStatus) == "" {
		return false, ErrInconclusive
	}
	newSt := state.Parse(info.TransactionStatus)
	code := rules.NormalizeError(info.ErrorName)
	err = e.Store.Tx(ctx, func(tx *sql.Tx) error {
		it, err := store.GetItem(ctx, tx, itemID, true)
		if err != nil {
			return err
		}
		if it.PayPalItemID == "" && info.PayoutItemID != "" && it.Mode != state.ModeSim {
			if err := store.SetItemPayPalIDs(ctx, tx, it.ID, info.PayoutItemID, it.Status, it.ErrorCode); err != nil {
				return err
			}
		}
		switch {
		case newSt == it.Status:
			if code != "" && code != it.ErrorCode {
				return store.UpdateItemStatus(ctx, tx, it.ID, it.Status, code)
			}
			return nil
		case state.CanTransition(it.Status, newSt):
			if code == "" && newSt != state.Failed {
				code = it.ErrorCode
			}
			if err := store.UpdateItemStatus(ctx, tx, it.ID, newSt, code); err != nil {
				return err
			}
			changed = true
			return store.AppendAudit(ctx, tx, actor, "status_changed", it.ID, map[string]any{"from": it.Status, "to": newSt, "error": code, "mode": it.Mode})
		default:
			if err := store.SetNeedsReview(ctx, tx, it.ID, true); err != nil {
				return err
			}
			return store.AppendAudit(ctx, tx, actor, "illegal_transition_ignored", it.ID, map[string]any{"from": it.Status, "to": newSt})
		}
	})
	if changed {
		e.emit("item", map[string]any{"id": itemID})
	}
	return changed, err
}

// ProcessItem reads one item back from PayPal, applies it, and proposes actions.
func (e *Engine) ProcessItem(ctx context.Context, itemID string) error {
	it, err := store.GetItem(ctx, e.Store.DB, itemID, false)
	if err != nil {
		return err
	}
	if it.PayPalItemID == "" && it.Mode != state.ModeSim {
		b, err := store.GetBatch(ctx, e.Store.DB, it.BatchID)
		if err != nil {
			return err
		}
		return e.syncBatch(ctx, b)
	}
	info, err := e.readItem(ctx, it)
	if err != nil {
		return err
	}
	if _, err := e.applyInfo(ctx, itemID, info, "paypal"); err != nil {
		return err
	}
	return e.Evaluate(ctx, itemID)
}

// syncBatch reads a whole batch (paginated) and applies every item.
func (e *Engine) syncBatch(ctx context.Context, b *store.Batch) error {
	if b.PayPalBatchID == "" || b.Mode == state.ModeSim {
		return nil
	}
	c, err := e.client(b.Mode)
	if err != nil {
		return err
	}
	res, err := c.GetBatch(ctx, b.PayPalBatchID)
	if err != nil {
		return err
	}
	items, err := store.ListItems(ctx, e.Store.DB, store.ItemFilter{BatchID: b.ID, Limit: 2000})
	if err != nil {
		return err
	}
	bySender := map[string]paypal.ItemInfo{}
	for _, pi := range res.Items {
		bySender[pi.SenderItemID] = pi
	}
	for _, it := range items {
		pi, ok := bySender[it.SenderItemID]
		if !ok {
			continue
		}
		if _, err := e.applyInfo(ctx, it.ID, pi, "paypal"); err != nil {
			e.log().Warn("apply failed", "item", it.ID, "err", err)
			continue
		}
		if err := e.Evaluate(ctx, it.ID); err != nil {
			e.log().Warn("evaluate failed", "item", it.ID, "err", err)
		}
	}
	return nil
}

// ---------- proposing actions ----------

func facts(it *store.Item, platform, replyTo string) ai.Facts {
	return ai.Facts{Name: it.RecipientName, Amount: it.Amount, Currency: it.Currency, Platform: platform,
		DaysLeft: strconv.Itoa(it.DaysLeft), ReplyTo: replyTo}
}

func actionKey(it *store.Item, p rules.Proposal) string {
	switch p.Kind {
	case rules.KindReissue:
		return fmt.Sprintf("%s:reissue:a%d:%s", it.ID, it.Attempt, strings.ToLower(it.CorrectedEmail))
	case rules.KindNotify:
		return fmt.Sprintf("%s:notify:%s", it.ID, p.TemplateKey)
	case rules.KindCancel:
		return fmt.Sprintf("%s:cancel:%s", it.ID, strings.ToLower(it.CorrectedEmail))
	}
	h := sha1.Sum([]byte(p.Reason))
	return fmt.Sprintf("%s:%s:%s", it.ID, p.Kind, hex.EncodeToString(h[:4]))
}

// Evaluate applies the rules to an item and stores any new proposals.
func (e *Engine) Evaluate(ctx context.Context, itemID string) error {
	it, err := store.GetItem(ctx, e.Store.DB, itemID, false)
	if err != nil {
		return err
	}
	props := rules.Decide(rules.Facts{Status: it.Status, ErrorCode: it.ErrorCode, AgeDays: it.AgeDays, Attempt: it.Attempt, CorrectedEmail: it.CorrectedEmail, NeedsReview: it.NeedsReview})
	for _, p := range props {
		a := &store.Action{ItemID: it.ID, Kind: string(p.Kind), TemplateKey: p.TemplateKey, Reason: p.Reason, IdempotencyKey: actionKey(it, p), ProposedBy: "rules"}
		if p.Kind == rules.KindFlag {
			a.Status = "executed"
		}
		var subject, body string
		if p.Kind == rules.KindNotify {
			if subject, body, err = ai.Draft(ctx, e.AI, p.TemplateKey, facts(it, e.Platform, e.ReplyTo), "en"); err != nil {
				return err
			}
		}
		var inserted bool
		err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
			var err error
			if inserted, err = store.InsertAction(ctx, tx, a); err != nil || !inserted {
				return err
			}
			if p.Kind == rules.KindNotify {
				if err := store.InsertMessage(ctx, tx, &store.Message{ItemID: it.ID, ActionID: a.ID, Direction: "outbound", Subject: subject, Body: body, Status: "draft"}); err != nil {
					return err
				}
			}
			return store.AppendAudit(ctx, tx, "rules", "action_proposed", it.ID, map[string]any{"kind": p.Kind, "template": p.TemplateKey, "reason": p.Reason})
		})
		if err != nil {
			return err
		}
		if inserted {
			e.emit("action", map[string]any{"item": it.ID, "kind": p.Kind})
		}
	}
	return nil
}
