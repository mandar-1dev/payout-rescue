package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mandar-1dev/payoutrescue/internal/ai"
	"github.com/mandar-1dev/payoutrescue/internal/rules"
	"github.com/mandar-1dev/payoutrescue/internal/state"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

// A conflict is a payout where the evidence disagrees or a person must decide.
// Resolutions NEVER create, cancel or re-send a payout directly (see AGENTS.md).
// They may record a validated email, reject proposals, add a draft message,
// close the conflict, or create an ordinary proposal that still needs approval.

const (
	ConflictClaimRace          = "claim_race"
	ConflictAmbiguousReply     = "ambiguous_reply"
	ConflictEmailSameAsOnFile  = "email_same_as_on_file"
	ConflictRefusalVsProposal  = "refusal_vs_proposal"
	ConflictHeldWithCorrection = "held_with_correction"
	ConflictAttemptsExhausted  = "attempts_exhausted"
	ConflictStatusDisagreement = "status_disagreement"
)

// AllowedResolutions maps each conflict kind to its permitted resolutions.
var AllowedResolutions = map[string][]string{
	ConflictClaimRace:          {"acknowledge"},
	ConflictAmbiguousReply:     {"use_email", "ask_again", "dismiss"},
	ConflictEmailSameAsOnFile:  {"ask_again", "dismiss"},
	ConflictRefusalVsProposal:  {"withdraw_proposals", "keep_proposals"},
	ConflictHeldWithCorrection: {"wait", "escalate"},
	ConflictAttemptsExhausted:  {"close_unpaid", "paid_manually"},
	ConflictStatusDisagreement: {"refresh_now", "accept_and_close"},
}

// NoteRequired reports whether a resolution must carry an explanation.
func NoteRequired(kind, resolution string) bool {
	return (kind == ConflictRefusalVsProposal && resolution == "keep_proposals") ||
		(kind == ConflictAttemptsExhausted && resolution == "paid_manually")
}

// OpenConflict inserts a conflict, deduplicated by dedupeKey. It reports whether a new one was opened.
func (e *Engine) OpenConflict(ctx context.Context, tx *sql.Tx, itemID, kind, summary string, details map[string]any, dedupeKey string) (bool, error) {
	d, _ := json.Marshal(details)
	c := &store.Conflict{ItemID: itemID, Kind: kind, Summary: summary, Details: d, DedupeKey: dedupeKey}
	inserted, err := store.InsertConflict(ctx, tx, c)
	if err != nil || !inserted {
		return false, err
	}
	if err := store.AppendAudit(ctx, tx, "system", "conflict_opened", itemID, map[string]any{"kind": kind, "conflict_id": c.ID, "summary": summary}); err != nil {
		return false, err
	}
	return true, nil
}

// detect runs fn in a transaction and announces the conflict only if one was really opened.
func (e *Engine) detect(ctx context.Context, itemID, kind string, fn func(tx *sql.Tx) (bool, error)) {
	var opened bool
	err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		opened, err = fn(tx)
		return err
	})
	if err != nil {
		e.log().Warn("conflict detection failed", "kind", kind, "item", itemID, "err", err)
		return
	}
	if opened {
		e.emit("conflict", map[string]any{"item": itemID, "kind": kind})
	}
}

// ResolveConflict validates and resolves a conflict on behalf of a named person.
func (e *Engine) ResolveConflict(ctx context.Context, conflictID, approver, resolution, value, note string) error {
	approver = strings.TrimSpace(approver)
	if approver == "" {
		return errors.New("an approver name is required")
	}
	c, err := store.GetConflict(ctx, e.Store.DB, conflictID)
	if err != nil {
		return err
	}
	if c.Status != "open" {
		return errors.New("conflict is already resolved")
	}
	allowed := AllowedResolutions[c.Kind]
	valid := false
	for _, r := range allowed {
		if r == resolution {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("resolution %q is not allowed for conflict kind %q; allowed: %s", resolution, c.Kind, strings.Join(allowed, ", "))
	}
	if NoteRequired(c.Kind, resolution) && strings.TrimSpace(note) == "" {
		return errors.New("a note is required for this resolution")
	}
	if c.Kind == ConflictStatusDisagreement && resolution == "refresh_now" {
		if err := e.refreshForConflict(ctx, c.ItemID); err != nil {
			return err
		}
	}
	if err := e.applyResolution(ctx, c, approver, resolution, strings.TrimSpace(value), note); err != nil {
		return err
	}
	e.emit("conflict", map[string]any{"item": c.ItemID, "kind": c.Kind, "resolution": resolution, "id": c.ID})
	switch resolution {
	case "use_email", "accept_and_close", "refresh_now":
		return e.Evaluate(ctx, c.ItemID) // new proposals still need ordinary approval
	}
	return nil
}

// refreshForConflict re-reads the item and applies PayPal's answer, but only if that answer is a legal change.
func (e *Engine) refreshForConflict(ctx context.Context, itemID string) error {
	it, err := store.GetItem(ctx, e.Store.DB, itemID, false)
	if err != nil {
		return err
	}
	info, err := e.readItem(ctx, it)
	if err != nil {
		return fmt.Errorf("could not read the payout back from PayPal: %w", err)
	}
	if strings.TrimSpace(info.TransactionStatus) == "" {
		return ErrInconclusive
	}
	if next := state.Parse(info.TransactionStatus); !state.CanTransition(it.Status, next) {
		return fmt.Errorf("PayPal still reports %s, which cannot follow %s. Investigate, then choose accept_and_close", next, it.Status)
	}
	if _, err := e.applyInfo(ctx, itemID, info, "paypal"); err != nil {
		return err
	}
	return e.Store.Tx(ctx, func(tx *sql.Tx) error { return store.SetNeedsReview(ctx, tx, itemID, false) })
}

func (e *Engine) applyResolution(ctx context.Context, c *store.Conflict, approver, resolution, value, note string) error {
	return e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.ResolveConflict(ctx, tx, c.ID, resolution, approver, note); err != nil {
			return err
		}
		switch resolution {
		case "use_email":
			var details map[string]any
			_ = json.Unmarshal(c.Details, &details)
			candidates, _ := details["candidates"].([]any)
			found := false
			for _, cand := range candidates {
				if s, ok := cand.(string); ok && strings.EqualFold(s, value) {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("email %q is not one of the candidates", value)
			}
			if !ai.ValidEmail(value) {
				return fmt.Errorf("email %q is not valid", value)
			}
			it, err := store.GetItem(ctx, tx, c.ItemID, true)
			if err != nil {
				return err
			}
			if strings.EqualFold(value, it.RecipientEmail) {
				return errors.New("that is the email already on file")
			}
			if err := store.SetCorrectedEmail(ctx, tx, c.ItemID, value); err != nil {
				return err
			}
			if err := store.AppendAudit(ctx, tx, approver, "corrected_email_recorded", c.ItemID, map[string]any{
				"email_domain": value[strings.LastIndex(value, "@")+1:], "source": "conflict_resolution",
			}); err != nil {
				return err
			}
		case "ask_again":
			tpl := "CLARIFY_EMAIL"
			if c.Kind == ConflictEmailSameAsOnFile {
				tpl = "SAME_EMAIL_NOTICE"
			}
			it, err := store.GetItem(ctx, tx, c.ItemID, false)
			if err != nil {
				return err
			}
			subject, body, err := ai.Draft(ctx, nil, tpl, facts(it, e.Platform, e.ReplyTo), "en")
			if err != nil {
				return err
			}
			if err := store.InsertMessage(ctx, tx, &store.Message{ItemID: c.ItemID, Direction: "outbound", Subject: subject, Body: body, Status: "draft"}); err != nil {
				return err
			}
		case "withdraw_proposals":
			as, err := store.ListActions(ctx, tx, "proposed", c.ItemID, 100)
			if err != nil {
				return err
			}
			for _, a := range as {
				if !rules.Kind(a.Kind).MoneyMoving() {
					continue
				}
				if err := store.SetActionStatus(ctx, tx, a.ID, "rejected", approver, map[string]any{"reason": "recipient_refusal"}); err != nil {
					return err
				}
				if err := store.AppendAudit(ctx, tx, approver, "action_rejected", a.ItemID, map[string]any{"action": a.ID, "kind": a.Kind, "reason": "recipient_refusal"}); err != nil {
					return err
				}
			}
		case "escalate":
			it, err := store.GetItem(ctx, tx, c.ItemID, false)
			if err != nil {
				return err
			}
			a := &store.Action{ItemID: it.ID, Kind: string(rules.KindEscalate), Reason: "Escalated from conflict: " + c.Summary,
				IdempotencyKey: fmt.Sprintf("%s:escalate:conflict:%s", it.ID, c.ID), ProposedBy: "conflict_resolution"}
			if _, err := store.InsertAction(ctx, tx, a); err != nil {
				return err
			}
		case "accept_and_close":
			if err := store.SetNeedsReview(ctx, tx, c.ItemID, false); err != nil {
				return err
			}
		}
		return store.AppendAudit(ctx, tx, approver, "conflict_resolved", c.ItemID, map[string]any{
			"conflict_id": c.ID, "kind": c.Kind, "resolution": resolution, "value": value, "note": note,
		})
	})
}

// ---------- detectors ----------

func (e *Engine) detectClaimRace(ctx context.Context, itemID, actionKind string) {
	e.detect(ctx, itemID, ConflictClaimRace, func(tx *sql.Tx) (bool, error) {
		it, err := store.GetItem(ctx, tx, itemID, false)
		if err != nil || it.Status != state.Success {
			return false, err
		}
		return e.OpenConflict(ctx, tx, itemID, ConflictClaimRace,
			fmt.Sprintf("A %s action was refused because the recipient claimed the payment", actionKind),
			map[string]any{"action_kind": actionKind, "status": string(it.Status)}, "claim_race:"+itemID)
	})
}

func (e *Engine) detectAmbiguousReply(ctx context.Context, itemID string, candidates []string) {
	sorted := append([]string(nil), candidates...)
	sort.Strings(sorted)
	e.detect(ctx, itemID, ConflictAmbiguousReply, func(tx *sql.Tx) (bool, error) {
		return e.OpenConflict(ctx, tx, itemID, ConflictAmbiguousReply, "A reply contained more than one email address",
			map[string]any{"candidates": sorted}, fmt.Sprintf("ambiguous_reply:%s:%s", itemID, strings.Join(sorted, ",")))
	})
}

func (e *Engine) detectEmailSameAsOnFile(ctx context.Context, itemID string) {
	e.detect(ctx, itemID, ConflictEmailSameAsOnFile, func(tx *sql.Tx) (bool, error) {
		return e.OpenConflict(ctx, tx, itemID, ConflictEmailSameAsOnFile, "The reply gave the same email we already have on file",
			map[string]any{}, "email_same:"+itemID)
	})
}

func (e *Engine) detectRefusalVsProposal(ctx context.Context, itemID string) {
	e.detect(ctx, itemID, ConflictRefusalVsProposal, func(tx *sql.Tx) (bool, error) {
		as, err := store.ListActions(ctx, tx, "proposed", itemID, 100)
		if err != nil {
			return false, err
		}
		open := false
		for _, a := range as {
			if rules.Kind(a.Kind).MoneyMoving() {
				open = true
			}
		}
		if !open {
			return false, nil
		}
		return e.OpenConflict(ctx, tx, itemID, ConflictRefusalVsProposal,
			"The recipient said they do not want the payment while money-moving proposals are open", map[string]any{}, "refusal_vs_proposal:"+itemID)
	})
}

func (e *Engine) detectHeldWithCorrection(ctx context.Context, itemID string) {
	e.detect(ctx, itemID, ConflictHeldWithCorrection, func(tx *sql.Tx) (bool, error) {
		it, err := store.GetItem(ctx, tx, itemID, false)
		if err != nil || (it.Status != state.OnHold && it.Status != state.Blocked) || it.CorrectedEmail == "" {
			return false, err
		}
		return e.OpenConflict(ctx, tx, itemID, ConflictHeldWithCorrection,
			fmt.Sprintf("The payout is %s at PayPal but a corrected email exists", strings.ToLower(string(it.Status))),
			map[string]any{"status": string(it.Status), "corrected_email_domain": it.CorrectedEmail[strings.LastIndex(it.CorrectedEmail, "@")+1:]},
			"held_with_correction:"+itemID)
	})
}

func (e *Engine) detectAttemptsExhausted(ctx context.Context, itemID string) {
	e.detect(ctx, itemID, ConflictAttemptsExhausted, func(tx *sql.Tx) (bool, error) {
		it, err := store.GetItem(ctx, tx, itemID, false)
		if err != nil || !it.Status.ReissueSafe() || it.Attempt < rules.MaxAttempts || it.CorrectedEmail == "" {
			return false, err
		}
		return e.OpenConflict(ctx, tx, itemID, ConflictAttemptsExhausted,
			fmt.Sprintf("This payout has used all %d attempts but a corrected email exists", it.Attempt),
			map[string]any{"attempt": it.Attempt, "max": rules.MaxAttempts}, "attempts_exhausted:"+itemID)
	})
}

func (e *Engine) detectStatusDisagreement(ctx context.Context, itemID string) {
	e.detect(ctx, itemID, ConflictStatusDisagreement, func(tx *sql.Tx) (bool, error) {
		return e.OpenConflict(ctx, tx, itemID, ConflictStatusDisagreement,
			"PayPal reported a status change that cannot happen, so the payout was flagged for review", map[string]any{}, "status_disagreement:"+itemID)
	})
}

// supersedeStaleProposals rejects proposed money-moving actions that no longer qualify
// (for example a cancel whose payout was claimed meanwhile), and records a claim race.
func (e *Engine) supersedeStaleProposals(ctx context.Context, itemID string) {
	opened := false
	err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
		it, err := store.GetItem(ctx, tx, itemID, false)
		if err != nil {
			return err
		}
		as, err := store.ListActions(ctx, tx, "proposed", itemID, 100)
		if err != nil {
			return err
		}
		for _, a := range as {
			stale := false
			switch rules.Kind(a.Kind) {
			case rules.KindCancel:
				stale = it.Status != state.Unclaimed
			case rules.KindReissue:
				stale = !it.Status.ReissueSafe()
			}
			if !stale {
				continue
			}
			if err := store.SetActionStatus(ctx, tx, a.ID, "rejected", "system", map[string]any{"reason": "superseded", "current_status": string(it.Status)}); err != nil {
				return err
			}
			if err := store.AppendAudit(ctx, tx, "system", "action_superseded", a.ItemID, map[string]any{"action": a.ID, "kind": a.Kind, "current_status": string(it.Status)}); err != nil {
				return err
			}
			if rules.Kind(a.Kind) == rules.KindCancel && it.Status == state.Success {
				ok, err := e.OpenConflict(ctx, tx, itemID, ConflictClaimRace, "A cancel was proposed, then the recipient claimed the payment",
					map[string]any{"action_id": a.ID, "status": string(it.Status)}, "claim_race:"+itemID)
				if err != nil {
					return err
				}
				opened = opened || ok
			}
		}
		return nil
	})
	if err != nil {
		e.log().Warn("superseding stale proposals failed", "item", itemID, "err", err)
		return
	}
	if opened {
		e.emit("conflict", map[string]any{"item": itemID, "kind": ConflictClaimRace})
	}
}
