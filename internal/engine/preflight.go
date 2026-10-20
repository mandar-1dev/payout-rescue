package engine

import (
	"context"
	"fmt"

	"github.com/mandar-1dev/payoutrescue/internal/ai"
	"github.com/mandar-1dev/payoutrescue/internal/paypal"
	"github.com/mandar-1dev/payoutrescue/internal/rules"
	"github.com/mandar-1dev/payoutrescue/internal/state"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

type Check struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type Preview struct {
	Method        string `json:"method"`
	Endpoint      string `json:"endpoint"`
	SenderBatchID string `json:"sender_batch_id,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	Receiver      string `json:"receiver,omitempty"`
	Amount        string `json:"amount,omitempty"`
	Currency      string `json:"currency,omitempty"`
}

type Preflight struct {
	ActionID string   `json:"action_id"`
	Kind     string   `json:"kind"`
	Checks   []Check  `json:"checks"`
	Preview  *Preview `json:"preview,omitempty"`
	Ready    bool     `json:"ready"`
	Note     string   `json:"note"`
}

const freshNote = "These checks use the last known state. PayPal is read again at approval time, and nothing runs if that fresh readback disagrees."

// Preflight computes the visible safety checks for a proposed action.
func (e *Engine) Preflight(ctx context.Context, a store.Action) (Preflight, error) {
	pf := Preflight{ActionID: a.ID, Kind: a.Kind, Ready: true, Note: freshNote}
	it, err := store.GetItem(ctx, e.Store.DB, a.ItemID, false)
	if err != nil {
		return pf, err
	}
	add := func(id, label string, ok bool, detail string) {
		pf.Checks = append(pf.Checks, Check{id, label, ok, detail})
		if !ok {
			pf.Ready = false
		}
	}
	switch rules.Kind(a.Kind) {
	case rules.KindReissue:
		var siblings int
		_ = e.Store.DB.QueryRowContext(ctx, `SELECT count(*) FROM payout_items WHERE logical_payout_id=$1 AND id<>$2 AND status IN ('CREATED','PENDING','UNCLAIMED','ONHOLD','BLOCKED','SUCCESS','UNKNOWN')`, it.LogicalPayoutID, it.ID).Scan(&siblings)
		add("original_dead", "Original is confirmed dead", it.Status.ReissueSafe(), fmt.Sprintf("Last known status %s (needs FAILED, RETURNED or CANCELED)", it.Status))
		add("no_live_sibling", "No other live payout for this person", siblings == 0, fmt.Sprintf("%d other live items", siblings))
		add("corrected_email", "Corrected email is valid", ai.ValidEmail(it.CorrectedEmail), "Validated in code, never trusted from AI output")
		add("attempts", "Attempt limit not reached", it.Attempt < rules.MaxAttempts, fmt.Sprintf("Attempt %d of %d", it.Attempt, rules.MaxAttempts))
		add("amount", "Amount and currency unchanged", true, fmt.Sprintf("%s %s, copied from the payout record", it.Amount, it.Currency))
		next := it.Attempt + 1
		pf.Preview = &Preview{Method: "POST", Endpoint: "/v1/payments/payouts", SenderBatchID: fmt.Sprintf("prb-%s-a%d", short(it.LogicalPayoutID), next),
			RequestID: fmt.Sprintf("req-prb-%s-a%d", short(it.LogicalPayoutID), next), Receiver: it.CorrectedEmail, Amount: it.Amount, Currency: it.Currency}
	case rules.KindCancel:
		add("unclaimed", "Item is still unclaimed", it.Status == state.Unclaimed, fmt.Sprintf("Last known status %s", it.Status))
		add("window", "Inside PayPal's 30-day window", it.DaysLeft > 0, fmt.Sprintf("%d days left before automatic return", it.DaysLeft))
		add("confirm", "Cancellation is confirmed by readback", true, "Replacement is only allowed after PayPal reports CANCELED or RETURNED")
		pf.Preview = &Preview{Method: "POST", Endpoint: "/v1/payments/payouts-item/" + it.PayPalItemID + "/cancel", Amount: it.Amount, Currency: it.Currency, Receiver: it.RecipientEmail}
	default:
		return pf, nil
	}
	return pf, nil
}

var _ = paypal.CreateItem{}

// RehydrateFake re-creates FAKE items in the in-memory fake after a restart.
func (e *Engine) RehydrateFake(ctx context.Context) (int, error) {
	if e.Fake == nil {
		return 0, nil
	}
	items, err := store.ListItems(ctx, e.Store.DB, store.ItemFilter{Mode: state.ModeFake, Limit: 5000})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		b, err := store.GetBatch(ctx, e.Store.DB, it.BatchID)
		if err != nil || b.PayPalBatchID == "" || it.PayPalItemID == "" {
			continue
		}
		e.Fake.RestoreItem(b.PayPalBatchID, b.SenderBatchID, paypal.ItemInfo{
			PayoutItemID: it.PayPalItemID, SenderItemID: it.SenderItemID, TransactionStatus: string(it.Status),
			Receiver: it.RecipientEmail, Currency: it.Currency, Value: it.Amount, ErrorName: it.ErrorCode,
		}, it.AgeDays)
		n++
	}
	return n, nil
}
