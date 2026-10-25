package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mandar-1dev/payoutrescue/internal/ai"
	"github.com/mandar-1dev/payoutrescue/internal/state"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

var ErrBadEvent = errors.New("webhook body is not a valid PayPal event")

type webhookBody struct {
	ID        string `json:"id"`
	EventType string `json:"event_type"`
	Resource  struct {
		PayoutItemID  string `json:"payout_item_id"`
		PayoutBatchID string `json:"payout_batch_id"`
		BatchHeader   struct {
			PayoutBatchID string `json:"payout_batch_id"`
		} `json:"batch_header"`
	} `json:"resource"`
}

// IngestWebhook verifies and stores an event exactly once. Processing is a
// separate step (ProcessEvent) so the HTTP handler can answer PayPal quickly.
func (e *Engine) IngestWebhook(ctx context.Context, h http.Header, body []byte) (eventID string, dup bool, err error) {
	var wb webhookBody
	if err := json.Unmarshal(body, &wb); err != nil || wb.ID == "" || wb.EventType == "" {
		return "", false, ErrBadEvent
	}
	verified := false
	if !e.WebhookBypass {
		if e.Live == nil {
			return wb.ID, false, ErrBadSignature
		}
		ok, verr := e.Live.VerifyWebhook(ctx, h, body, e.WebhookID)
		if verr != nil || !ok {
			return wb.ID, false, ErrBadSignature
		}
		verified = true
	}
	res := wb.Resource.PayoutItemID
	if res == "" {
		res = wb.Resource.PayoutBatchID
	}
	inserted, err := store.InsertWebhookEvent(ctx, e.Store.DB, store.EventRow{EventID: wb.ID, EventType: wb.EventType, ResourceID: res, Verified: verified, Payload: body})
	if err != nil {
		return wb.ID, false, err
	}
	if !inserted {
		return wb.ID, true, nil
	}
	_ = e.audit(ctx, "paypal", "webhook_received", "", map[string]any{"event_id": wb.ID, "type": wb.EventType, "verified": verified})
	return wb.ID, false, nil
}

// ProcessEvent applies a stored event. It never trusts the payload's status:
// the item is read back from PayPal first. A failure leaves the event
// unprocessed so the reconciler retries it.
func (e *Engine) ProcessEvent(ctx context.Context, eventID string) error {
	ev, err := store.GetEvent(ctx, e.Store.DB, eventID)
	if err != nil {
		return err
	}
	if ev.Processed {
		return nil
	}
	var wb webhookBody
	_ = json.Unmarshal(ev.Payload, &wb)
	var perr error
	matched := false
	if id := wb.Resource.PayoutItemID; id != "" {
		if it, ferr := store.FindItemByPayPalID(ctx, e.Store.DB, id); ferr == nil {
			matched = true
			perr = e.ProcessItem(ctx, it.ID)
		}
	}
	if !matched {
		bid := wb.Resource.BatchHeader.PayoutBatchID
		if bid == "" {
			bid = wb.Resource.PayoutBatchID
		}
		if bid != "" {
			if b, ferr := store.GetBatchByPayPalID(ctx, e.Store.DB, bid); ferr == nil {
				matched = true
				perr = e.syncBatch(ctx, b)
			}
		}
	}
	if perr != nil {
		return perr
	}
	note := ""
	if !matched {
		note = "no matching item or batch"
	}
	return store.MarkEventProcessed(ctx, e.Store.DB, eventID, note)
}

type ReconcileSummary struct {
	Batches int      `json:"batches"`
	Items   int      `json:"items"`
	Resumed int      `json:"resumed"`
	Events  int      `json:"events"`
	Errors  []string `json:"errors"`
}

// Reconcile recovers from missed or late webhooks, lost responses and crashes.
// It reads batches (not single items) to stay far below PayPal's rate limits.
func (e *Engine) Reconcile(ctx context.Context) ReconcileSummary {
	var s ReconcileSummary
	note := func(err error, what string) {
		if err != nil && !errors.Is(err, ErrWillRetry) {
			s.Errors = append(s.Errors, what+": "+err.Error())
		}
	}
	// 1. batches whose create call never completed
	if bs, err := store.BatchesCreatedBefore(ctx, e.Store.DB, time.Now().Add(-e.resumeAfter())); err == nil {
		for _, b := range bs {
			items, _ := store.ListItems(ctx, e.Store.DB, store.ItemFilter{BatchID: b.ID, Limit: 2000})
			if c, cerr := e.client(b.Mode); cerr == nil {
				bb := b
				note(e.sendBatch(ctx, c, &bb, items, nil), "resume batch "+b.ID)
				s.Resumed++
			}
		}
	}
	// 2. approved re-issues that never finished
	if as, err := store.ApprovedBefore(ctx, e.Store.DB, time.Now().Add(-e.resumeAfter())); err == nil {
		for _, a := range as {
			if a.Kind == "reissue" {
				note(e.runReissue(ctx, a.ID), "resume action "+a.ID)
				s.Resumed++
			} else if a.Kind == "cancel" {
				note(e.runCancel(ctx, a.ID), "resume action "+a.ID)
				s.Resumed++
			}
		}
	}
	// 3. stored events that were never processed
	if ids, err := store.UnprocessedEvents(ctx, e.Store.DB, time.Now().Add(-e.resumeAfter()/2)); err == nil {
		for _, id := range ids {
			note(e.ProcessEvent(ctx, id), "event "+id)
			s.Events++
		}
	}
	// 4. poll everything still moving
	items, err := store.ItemsNeedingPoll(ctx, e.Store.DB)
	if err != nil {
		note(err, "list items")
		return s
	}
	batches := map[string]*store.Batch{}
	for _, it := range items {
		s.Items++
		if it.Mode == state.ModeSim || it.PayPalItemID == "" && it.Mode == state.ModeSim {
			note(e.ProcessItem(ctx, it.ID), "item "+it.ID)
			continue
		}
		if _, ok := batches[it.BatchID]; !ok {
			if b, berr := store.GetBatch(ctx, e.Store.DB, it.BatchID); berr == nil {
				batches[it.BatchID] = b
			}
		}
	}
	for _, b := range batches {
		s.Batches++
		note(e.syncBatch(ctx, b), "batch "+b.ID)
	}
	return s
}

// ---------- recipient replies ----------

// SetCorrectedEmail records a validated corrected email (typed by an operator or
// extracted from a reply) and re-evaluates the item.
func (e *Engine) SetCorrectedEmail(ctx context.Context, itemID, email, actor string) error {
	email = strings.TrimSpace(email)
	if !ai.ValidEmail(email) {
		return errors.New("that is not a valid email address")
	}
	var sameEmail bool
	err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
		it, err := store.GetItem(ctx, tx, itemID, true)
		if err != nil {
			return err
		}
		if strings.EqualFold(email, it.RecipientEmail) {
			sameEmail = true
			return errors.New("the corrected email is the same as the one on file")
		}
		if err := store.SetCorrectedEmail(ctx, tx, it.ID, email); err != nil {
			return err
		}
		return store.AppendAudit(ctx, tx, actor, "corrected_email_recorded", it.ID, map[string]any{"email_domain": email[strings.LastIndex(email, "@")+1:]})
	})
	if sameEmail {
		e.detectEmailSameAsOnFile(ctx, itemID)
	}
	if err != nil {
		return err
	}
	return e.Evaluate(ctx, itemID)
}

// InboundReply stores a recipient reply, extracts data with the validated AI
// pipeline, and records a corrected email only if every check passes.
func (e *Engine) InboundReply(ctx context.Context, itemID, text string) (*store.Message, ai.Extraction, error) {
	var zero ai.Extraction
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 4000 {
		return nil, zero, errors.New("reply must be between 1 and 4000 characters")
	}
	if _, err := store.GetItem(ctx, e.Store.DB, itemID, false); err != nil {
		return nil, zero, err
	}
	ex := ai.Extract(ctx, e.AI, text)
	exJSON, _ := json.Marshal(ex)
	flags, _ := json.Marshal(map[string]bool{"injection": ex.Injection, "unclear": ex.Unclear})
	msg := &store.Message{ItemID: itemID, Direction: "inbound", Body: text, Status: "received", Extracted: exJSON, Flags: flags}
	err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.InsertMessage(ctx, tx, msg); err != nil {
			return err
		}
		event := "reply_received"
		if ex.Injection {
			event = "injection_blocked"
			if err := store.SetNeedsReview(ctx, tx, itemID, true); err != nil {
				return err
			}
		}
		return store.AppendAudit(ctx, tx, "recipient", event, itemID, map[string]any{"source": ex.Source, "has_email": ex.NewEmail != "", "unclear": ex.Unclear, "injection": ex.Injection})
	})
	if err != nil {
		return nil, ex, err
	}
	if ex.Injection {
		// Nothing from the reply is applied. Evaluate so the flagged item gets its escalation.
		if err := e.Evaluate(ctx, itemID); err != nil {
			return msg, ex, err
		}
	} else {
		if len(ex.Candidates) > 1 {
			e.detectAmbiguousReply(ctx, itemID, ex.Candidates)
		}
		if ex.Refuses {
			e.detectRefusalVsProposal(ctx, itemID)
		}
		recorded := false
		if ex.NewEmail != "" {
			if err := e.SetCorrectedEmail(ctx, itemID, ex.NewEmail, "extraction"); err != nil {
				e.log().Info("extracted email not recorded", "item", itemID, "err", err)
			} else {
				recorded = true
			}
		}
		if !recorded {
			if err := e.Evaluate(ctx, itemID); err != nil {
				return msg, ex, err
			}
		}
	}
	e.emit("item", map[string]any{"id": itemID})
	return msg, ex, nil
}

// AdvanceFake moves time forward for FAKE items only, so day 20/27/30 behaviour can be shown.
func (e *Engine) AdvanceFake(ctx context.Context, days int) error {
	if e.Fake == nil {
		return errors.New("fake PayPal is disabled")
	}
	if days < 1 || days > 35 {
		return errors.New("days must be between 1 and 35")
	}
	e.Fake.Advance(days)
	if err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.BackdateFake(ctx, tx, days); err != nil {
			return err
		}
		return store.AppendAudit(ctx, tx, "operator", "fake_time_advanced", "", map[string]any{"days": days, "note": "FAKE items only"})
	}); err != nil {
		return err
	}
	e.Reconcile(ctx)
	return nil
}

// ---------- dashboard summary ----------

type CurrencyTotal struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type Bucket struct {
	Label  string `json:"label"`
	Count  int    `json:"count"`
	Amount string `json:"amount"`
}

type Summary struct {
	InLimbo             []CurrencyTotal `json:"in_limbo"`
	ByStatus            map[string]int  `json:"by_status"`
	ByMode              map[string]int  `json:"by_mode"`
	Buckets             []Bucket        `json:"buckets"`
	NeedingAction       int             `json:"needing_action"`
	NextReturnDays      *int            `json:"next_return_days"`
	DuplicatesPrevented int             `json:"duplicates_prevented"`
	Items               int             `json:"items"`
	WebhookEvents       int             `json:"webhook_events"`
	WebhookUnprocessed  int             `json:"webhook_unprocessed"`
	AuditEntries        int             `json:"audit_entries"`
	AuditIntact         bool            `json:"audit_intact"`
	WebhookBypass       bool            `json:"webhook_bypass"`
	OpenConflicts       int             `json:"open_conflicts"`
}

func cents(s string) int64 {
	var whole, frac int64
	parts := strings.SplitN(s, ".", 2)
	fmt.Sscanf(parts[0], "%d", &whole)
	if len(parts) == 2 {
		f := (parts[1] + "00")[:2]
		fmt.Sscanf(f, "%d", &frac)
	}
	return whole*100 + frac
}

func money(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

func (e *Engine) Summary(ctx context.Context) (Summary, error) {
	s := Summary{ByStatus: map[string]int{}, ByMode: map[string]int{}, WebhookBypass: e.WebhookBypass}
	items, err := store.ListItems(ctx, e.Store.DB, store.ItemFilter{Limit: 5000})
	if err != nil {
		return s, err
	}
	totals := map[string]int64{}
	labels := []string{"0-7", "8-14", "15-21", "22-27", "28-30"}
	counts := make([]int, len(labels))
	amts := make([]int64, len(labels))
	s.Items = len(items)
	for _, it := range items {
		s.ByStatus[string(it.Status)]++
		s.ByMode[it.Mode]++
		switch it.Status {
		case state.Unclaimed, state.OnHold, state.Blocked, state.Pending, state.Created, state.Unknown:
			c := cents(it.Amount)
			totals[it.Currency] += c
			b := 4
			switch d := it.AgeDays; {
			case d <= 7:
				b = 0
			case d <= 14:
				b = 1
			case d <= 21:
				b = 2
			case d <= 27:
				b = 3
			}
			counts[b]++
			amts[b] += c
			if it.Status == state.Unclaimed && (s.NextReturnDays == nil || it.DaysLeft < *s.NextReturnDays) {
				d := it.DaysLeft
				s.NextReturnDays = &d
			}
		}
	}
	for cur, c := range totals {
		s.InLimbo = append(s.InLimbo, CurrencyTotal{Currency: cur, Amount: money(c)})
	}
	for i, l := range labels {
		s.Buckets = append(s.Buckets, Bucket{Label: l, Count: counts[i], Amount: money(amts[i])})
	}
	proposed, err := store.ListActions(ctx, e.Store.DB, "proposed", "", 1000)
	if err != nil {
		return s, err
	}
	for _, a := range proposed {
		s.NeedingAction++
		_ = a
	}
	s.DuplicatesPrevented, _ = store.CountAuditEvent(ctx, e.Store.DB, "duplicate_prevented")
	s.OpenConflicts, _ = store.CountOpenConflicts(ctx, e.Store.DB)
	s.WebhookEvents, s.WebhookUnprocessed, _ = store.CountEvents(ctx, e.Store.DB)
	s.AuditEntries, _, _ = store.VerifyAudit(ctx, e.Store.DB)
	_, broken, _ := store.VerifyAudit(ctx, e.Store.DB)
	s.AuditIntact = broken == 0
	return s, nil
}
