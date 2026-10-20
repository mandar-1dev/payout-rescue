package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mandar-1dev/payoutrescue/internal/ai"
	"github.com/mandar-1dev/payoutrescue/internal/paypal"
	"github.com/mandar-1dev/payoutrescue/internal/rules"
	"github.com/mandar-1dev/payoutrescue/internal/state"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

func (e *Engine) markApproved(ctx context.Context, actionID, actor string) error {
	return e.Store.Tx(ctx, func(tx *sql.Tx) error {
		a, err := store.GetAction(ctx, tx, actionID, true)
		if err != nil {
			return err
		}
		if a.Status != "proposed" {
			return fmt.Errorf("%w (status is %s)", ErrNotApprovable, a.Status)
		}
		if err := store.SetActionStatus(ctx, tx, a.ID, "approved", actor, nil); err != nil {
			return err
		}
		return store.AppendAudit(ctx, tx, actor, "action_approved", a.ItemID, map[string]any{"action": a.ID, "kind": a.Kind})
	})
}

func (e *Engine) finishAction(ctx context.Context, a *store.Action, status string, result map[string]any, event string) {
	err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.SetActionStatus(ctx, tx, a.ID, status, "", result); err != nil {
			return err
		}
		if status == "executed" && a.Kind == string(rules.KindNotify) {
			if err := store.SetMessageStatusByAction(ctx, tx, a.ID, "sent"); err != nil {
				return err
			}
		}
		return store.AppendAudit(ctx, tx, "system", event, a.ItemID, map[string]any{"action": a.ID, "kind": a.Kind, "result": result})
	})
	if err != nil {
		e.log().Error("finishing action failed", "action", a.ID, "err", err)
	}
	e.emit("action", map[string]any{"id": a.ID, "status": status})
}

// Approve records a human approval and executes the action. Money-moving actions
// first re-read PayPal; they never rely on a webhook or on cached state.
func (e *Engine) Approve(ctx context.Context, actionID, actor string) (*store.Action, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return nil, errors.New("an approver name is required")
	}
	a, err := store.GetAction(ctx, e.Store.DB, actionID, false)
	if err != nil {
		return nil, err
	}
	if a.Status != "proposed" {
		return a, fmt.Errorf("%w (status is %s)", ErrNotApprovable, a.Status)
	}
	if err := e.markApproved(ctx, a.ID, actor); err != nil {
		return nil, err
	}
	switch rules.Kind(a.Kind) {
	case rules.KindCancel:
		err = e.runCancel(ctx, a.ID)
	case rules.KindReissue:
		err = e.runReissue(ctx, a.ID)
	default: // notify, escalate, flag: acknowledgement only; nothing is emailed
		e.finishAction(ctx, a, "executed", map[string]any{"note": "acknowledged; messages are shown in the UI and are not emailed"}, "action_executed")
	}
	out, gerr := store.GetAction(ctx, e.Store.DB, actionID, false)
	if gerr != nil {
		return nil, gerr
	}
	return out, err
}

func (e *Engine) Reject(ctx context.Context, actionID, actor string) error {
	if strings.TrimSpace(actor) == "" {
		return errors.New("a name is required")
	}
	return e.Store.Tx(ctx, func(tx *sql.Tx) error {
		a, err := store.GetAction(ctx, tx, actionID, true)
		if err != nil {
			return err
		}
		if a.Status != "proposed" {
			return fmt.Errorf("%w (status is %s)", ErrNotApprovable, a.Status)
		}
		if err := store.SetActionStatus(ctx, tx, a.ID, "rejected", actor, nil); err != nil {
			return err
		}
		return store.AppendAudit(ctx, tx, actor, "action_rejected", a.ItemID, map[string]any{"action": a.ID, "kind": a.Kind})
	})
}

// ---------- cancel ----------

func (e *Engine) runCancel(ctx context.Context, actionID string) error {
	a, err := store.GetAction(ctx, e.Store.DB, actionID, false)
	if err != nil {
		return err
	}
	fail := func(msg string, cause error) error {
		e.finishAction(ctx, a, "failed", map[string]any{"error": msg}, "action_failed")
		return cause
	}
	it, err := store.GetItem(ctx, e.Store.DB, a.ItemID, false)
	if err != nil {
		return fail("item not found", err)
	}
	if it.Mode == state.ModeSim {
		return fail("cancel does not apply to simulated items", errors.New("not applicable"))
	}
	info, err := e.readItem(ctx, it)
	if err != nil {
		return fail("could not read the item back from PayPal: "+err.Error(), err)
	}
	if _, err := e.applyInfo(ctx, it.ID, info, "paypal"); err != nil {
		return fail("readback was inconclusive: "+err.Error(), err)
	}
	cur, _ := store.GetItem(ctx, e.Store.DB, it.ID, false)
	if cur.Status != state.Unclaimed {
		_ = e.audit(ctx, "system", "cancel_skipped", it.ID, map[string]any{"status": cur.Status})
		_ = e.Evaluate(ctx, it.ID)
		return fail(fmt.Sprintf("item is no longer unclaimed (status %s); nothing was cancelled", cur.Status), nil)
	}
	c, _ := e.client(cur.Mode)
	if _, cerr := c.CancelItem(ctx, cur.PayPalItemID); cerr != nil {
		var ae *paypal.APIError
		if errors.As(cerr, &ae) && (ae.Name == "ITEM_INCORRECT_STATUS" || ae.Name == "ITEM_ALREADY_CANCELLED") {
			if info2, rerr := e.readItem(ctx, cur); rerr == nil {
				_, _ = e.applyInfo(ctx, cur.ID, info2, "paypal")
			}
			_ = e.Evaluate(ctx, cur.ID)
			return fail("PayPal refused the cancel ("+ae.Name+"); state was refreshed", nil)
		}
		return fail("cancel call failed: "+cerr.Error(), cerr)
	}
	info3, err := e.readItem(ctx, cur)
	if err != nil {
		return fail("cancel sent but the readback failed; the reconciler will confirm: "+err.Error(), err)
	}
	if _, err := e.applyInfo(ctx, cur.ID, info3, "paypal"); err != nil {
		return fail("cancel readback inconclusive: "+err.Error(), err)
	}
	after, _ := store.GetItem(ctx, e.Store.DB, cur.ID, false)
	if after.Status != state.Canceled && after.Status != state.Returned {
		return fail(fmt.Sprintf("cancel was not confirmed by readback (status %s)", after.Status), nil)
	}
	e.finishAction(ctx, a, "executed", map[string]any{"confirmed_status": after.Status}, "action_executed")
	return e.Evaluate(ctx, cur.ID)
}

// ---------- reissue ----------

func (e *Engine) audit(ctx context.Context, actor, event, itemID string, details any) error {
	return e.Store.Tx(ctx, func(tx *sql.Tx) error { return store.AppendAudit(ctx, tx, actor, event, itemID, details) })
}

func (e *Engine) revertToProposed(ctx context.Context, a *store.Action, why string) {
	_ = e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.SetActionStatus(ctx, tx, a.ID, "proposed", "", map[string]any{"deferred": why}); err != nil {
			return err
		}
		return store.AppendAudit(ctx, tx, "system", "reissue_deferred", a.ItemID, map[string]any{"action": a.ID, "why": why})
	})
}

// runReissue creates exactly one replacement for a confirmed-dead item. It is
// resumable: ids are deterministic, so repeating it after a crash or a lost
// response can never create a second payout.
func (e *Engine) runReissue(ctx context.Context, actionID string) error {
	a, err := store.GetAction(ctx, e.Store.DB, actionID, false)
	if err != nil {
		return err
	}
	if a.Status != "approved" {
		return nil
	}
	fail := func(msg string, cause error) error {
		e.finishAction(ctx, a, "failed", map[string]any{"error": msg}, "action_failed")
		return cause
	}
	orig, err := store.GetItem(ctx, e.Store.DB, a.ItemID, false)
	if err != nil {
		return fail("item not found", err)
	}
	replSender := fmt.Sprintf("pr-%s-a%d", short(orig.LogicalPayoutID), orig.Attempt+1)
	repl, err := store.GetItemBySender(ctx, e.Store.DB, replSender)
	switch {
	case err == nil:
		// Reserved earlier (resume): skip straight to sending.
	case errors.Is(err, store.ErrNotFound):
		// Fresh, authoritative readback of the original.
		info, rerr := e.readItem(ctx, orig)
		if rerr != nil {
			e.revertToProposed(ctx, a, "readback failed: "+rerr.Error())
			return rerr
		}
		if _, aerr := e.applyInfo(ctx, orig.ID, info, "paypal"); aerr != nil {
			e.revertToProposed(ctx, a, "readback inconclusive: "+aerr.Error())
			return aerr
		}
		cur, _ := store.GetItem(ctx, e.Store.DB, orig.ID, false)
		if !cur.Status.ReissueSafe() {
			_ = e.audit(ctx, "system", "duplicate_prevented", cur.ID, map[string]any{"reason": "original not confirmed dead", "status": cur.Status})
			_ = e.Evaluate(ctx, cur.ID)
			return fail(fmt.Sprintf("original is %s, not confirmed FAILED, RETURNED or CANCELED; re-issue refused", cur.Status), nil)
		}
		if cur.Attempt >= rules.MaxAttempts || !ai.ValidEmail(cur.CorrectedEmail) {
			return fail("attempt limit reached or no valid corrected email", nil)
		}
		repl, err = e.reserveReplacement(ctx, cur)
		if errors.Is(err, store.ErrLiveItemExists) {
			_ = e.audit(ctx, "system", "duplicate_prevented", cur.ID, map[string]any{"reason": "database refused a second live item"})
			return fail("another live item already exists for this payout; re-issue refused", nil)
		}
		if err != nil {
			return fail("could not reserve the replacement: "+err.Error(), err)
		}
	default:
		return fail(err.Error(), err)
	}
	if repl.Status != state.Created {
		e.finishAction(ctx, a, "executed", map[string]any{"replacement": repl.ID, "note": "already sent"}, "action_executed")
		return nil
	}
	batch, err := store.GetBatch(ctx, e.Store.DB, repl.BatchID)
	if err != nil {
		return fail("replacement batch missing", err)
	}
	c, err := e.client(batch.Mode)
	if err != nil {
		return fail(err.Error(), err)
	}
	req := paypal.CreateBatchRequest{SenderBatchID: batch.SenderBatchID, EmailSubject: "You have a payout"}
	l, err := store.GetLogical(ctx, e.Store.DB, repl.LogicalPayoutID)
	if err != nil {
		return fail("logical payout missing", err)
	}
	note := ""
	if batch.Mode == state.ModeSim {
		note = simCreateNote
	}
	req.Items = []paypal.CreateItem{{SenderItemID: repl.SenderItemID, Receiver: repl.RecipientEmail, Currency: l.Currency, Value: l.Amount, Note: note}}
	res, err := c.CreateBatch(ctx, req, batch.RequestID)
	if err != nil {
		var ae *paypal.APIError
		if errors.As(err, &ae) && !ae.Retryable() {
			if ae.Name == "USER_BUSINESS_ERROR" || strings.Contains(strings.ToUpper(ae.Name), "DUPLICATE") {
				// PayPal may already have this batch. Fail closed: keep the live slot, flag for a human.
				_ = e.Store.Tx(ctx, func(tx *sql.Tx) error { return store.SetNeedsReview(ctx, tx, repl.ID, true) })
				return fail("PayPal reports this batch id already exists; verify manually before doing anything else", nil)
			}
			_ = e.Store.Tx(ctx, func(tx *sql.Tx) error { return store.UpdateItemStatus(ctx, tx, repl.ID, state.Failed, ae.Name) })
			return fail("PayPal rejected the replacement: "+ae.Name, nil)
		}
		e.log().Warn("replacement create unconfirmed; will retry with the same ids", "action", a.ID, "err", err)
		return ErrWillRetry
	}
	if err := e.afterCreate(ctx, batch, []store.Item{*repl}, res); err != nil {
		return err
	}
	_ = e.Store.Tx(ctx, func(tx *sql.Tx) error {
		cur, _ := store.GetItem(ctx, tx, repl.ID, false)
		subject, body, rerr := ai.Draft(ctx, nil, rules.TplReissueConfirm, facts(cur, e.Platform, e.ReplyTo), "en")
		if rerr != nil {
			return nil
		}
		return store.InsertMessage(ctx, tx, &store.Message{ItemID: cur.ID, ActionID: a.ID, Direction: "outbound", Subject: subject, Body: body, Status: "draft"})
	})
	e.finishAction(ctx, a, "executed", map[string]any{"replacement": repl.ID, "sender_batch_id": batch.SenderBatchID}, "action_executed")
	return nil
}

// reserveReplacement inserts the replacement batch and item in one transaction.
// The partial unique index makes a second live item for the same payout impossible.
func (e *Engine) reserveReplacement(ctx context.Context, orig *store.Item) (*store.Item, error) {
	attempt := orig.Attempt + 1
	var repl store.Item
	err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
		locked, err := store.GetItem(ctx, tx, orig.ID, true)
		if err != nil {
			return err
		}
		if !locked.Status.ReissueSafe() {
			return store.ErrLiveItemExists
		}
		b := &store.Batch{SenderBatchID: fmt.Sprintf("prb-%s-a%d", short(orig.LogicalPayoutID), attempt), RequestID: fmt.Sprintf("req-prb-%s-a%d", short(orig.LogicalPayoutID), attempt), Mode: orig.Mode}
		if err := store.InsertBatch(ctx, tx, b); err != nil {
			return err
		}
		repl = store.Item{LogicalPayoutID: orig.LogicalPayoutID, BatchID: b.ID, SenderItemID: fmt.Sprintf("pr-%s-a%d", short(orig.LogicalPayoutID), attempt),
			RecipientEmail: orig.CorrectedEmail, Attempt: attempt, OriginalItemID: orig.ID, Status: state.Created, Mode: orig.Mode}
		if orig.Mode == state.ModeSim {
			repl.SimTag = simItemSuccess
		}
		if err := store.InsertItem(ctx, tx, &repl, time.Now()); err != nil {
			return err
		}
		return store.AppendAudit(ctx, tx, "system", "reissue_reserved", orig.ID, map[string]any{"replacement": repl.ID, "attempt": attempt, "sender_batch_id": b.SenderBatchID})
	})
	if err != nil {
		return nil, err
	}
	return store.GetItem(ctx, e.Store.DB, repl.ID, false)
}
