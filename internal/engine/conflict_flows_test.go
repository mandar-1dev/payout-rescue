package engine_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/mandar-1dev/payoutrescue/internal/engine"
	"github.com/mandar-1dev/payoutrescue/internal/state"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

func openKinds(t *testing.T, st *store.Store, ctxItem string) []string {
	t.Helper()
	rows, err := st.DB.Query(`SELECT kind FROM conflicts WHERE status='open' ORDER BY kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		out = append(out, k)
	}
	_ = ctxItem
	return out
}

// Every kind must be produced by the real flows (replies, readbacks, approvals), once each.
func TestEveryConflictKindIsDetectedOnceByRealFlows(t *testing.T) {
	eng, st, ctx := setupEngineTest(t)
	if err := eng.SeedConflicts(ctx); err != nil {
		t.Fatal(err)
	}
	got := openKinds(t, st, "")
	want := []string{"ambiguous_reply", "attempts_exhausted", "claim_race", "email_same_as_on_file", "held_with_correction", "refusal_vs_proposal", "status_disagreement"}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("open conflicts %v, want exactly %v", got, want)
	}
	// Reconciling again must not open duplicates.
	eng.Reconcile(ctx)
	if again := openKinds(t, st, ""); len(again) != 7 {
		t.Fatalf("reconcile changed the number of open conflicts: %v", again)
	}
	if err := eng.SeedConflicts(ctx); err != engine.ErrAlreadySeeded {
		t.Fatalf("second seed must report already seeded, got %v", err)
	}
	// Nothing was paid or sent twice while producing the conflicts.
	if d := eng.Fake.Duplicates(); len(d) != 0 {
		t.Fatalf("duplicates while seeding: %v", d)
	}
	var live int
	if err := st.DB.QueryRow(`SELECT count(*) FROM (SELECT logical_payout_id FROM payout_items WHERE status IN ('CREATED','PENDING','UNCLAIMED','ONHOLD','BLOCKED','SUCCESS','UNKNOWN') GROUP BY 1 HAVING count(*) > 1) x`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("more than one live item for a payout: %d (%v)", live, err)
	}
}

func conflictOf(t *testing.T, st *store.Store, kind string) *store.Conflict {
	t.Helper()
	var id string
	if err := st.DB.QueryRow(`SELECT id FROM conflicts WHERE kind=$1 AND status='open'`, kind).Scan(&id); err != nil {
		t.Fatalf("no open %s conflict: %v", kind, err)
	}
	c, err := store.GetConflict(context.Background(), st.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestResolutionRulesPerKind(t *testing.T) {
	eng, st, ctx := setupEngineTest(t)
	if err := eng.SeedConflicts(ctx); err != nil {
		t.Fatal(err)
	}
	statusOf := func(itemID string) state.Status {
		it, err := store.GetItem(ctx, st.DB, itemID, false)
		if err != nil {
			t.Fatal(err)
		}
		return it.Status
	}

	// A name is always required, and a disallowed resolution is always refused.
	amb := conflictOf(t, st, "ambiguous_reply")
	if err := eng.ResolveConflict(ctx, amb.ID, "", "dismiss", "", ""); err == nil {
		t.Fatal("a resolution without a name must be refused")
	}
	if err := eng.ResolveConflict(ctx, amb.ID, "Ana", "paid_manually", "", "x"); err == nil {
		t.Fatal("a resolution that does not belong to this kind must be refused")
	}
	// use_email only accepts one of the candidates.
	if err := eng.ResolveConflict(ctx, amb.ID, "Ana", "use_email", "evil@attacker.example", ""); err == nil {
		t.Fatal("an email that is not a candidate must be refused")
	}
	before := statusOf(amb.ItemID)
	if err := eng.ResolveConflict(ctx, amb.ID, "Ana", "use_email", "omar.work@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if statusOf(amb.ItemID) != before {
		t.Fatal("use_email must not change the payout status")
	}
	it, _ := store.GetItem(ctx, st.DB, amb.ItemID, false)
	if it.CorrectedEmail != "omar.work@example.com" {
		t.Fatalf("corrected email not recorded: %q", it.CorrectedEmail)
	}
	if err := eng.ResolveConflict(ctx, amb.ID, "Ana", "dismiss", "", ""); err == nil {
		t.Fatal("an already resolved conflict must not be resolved again")
	}

	// Notes are mandatory for keep_proposals and paid_manually.
	ref := conflictOf(t, st, "refusal_vs_proposal")
	if err := eng.ResolveConflict(ctx, ref.ID, "Ana", "keep_proposals", "", "  "); err == nil {
		t.Fatal("keep_proposals needs a note")
	}
	if err := eng.ResolveConflict(ctx, ref.ID, "Ana", "withdraw_proposals", "", ""); err != nil {
		t.Fatal(err)
	}
	var open int
	_ = st.DB.QueryRow(`SELECT count(*) FROM actions WHERE item_id=$1 AND status='proposed' AND kind IN ('cancel','reissue')`, ref.ItemID).Scan(&open)
	if open != 0 {
		t.Fatalf("withdraw_proposals left %d money-moving proposals open", open)
	}

	ex := conflictOf(t, st, "attempts_exhausted")
	if err := eng.ResolveConflict(ctx, ex.ID, "Ana", "paid_manually", "", ""); err == nil {
		t.Fatal("paid_manually needs a note")
	}
	if err := eng.ResolveConflict(ctx, ex.ID, "Ana", "paid_manually", "", "Sent by bank transfer, reference 123"); err != nil {
		t.Fatal(err)
	}
	var attempts int
	_ = st.DB.QueryRow(`SELECT count(*) FROM payout_items WHERE logical_payout_id=(SELECT logical_payout_id FROM payout_items WHERE id=$1)`, ex.ItemID).Scan(&attempts)
	if attempts != 3 {
		t.Fatalf("resolving attempts_exhausted must never create a fourth attempt, found %d items", attempts)
	}

	// refresh_now refuses while PayPal still reports an impossible change, and accept_and_close clears the flag.
	dis := conflictOf(t, st, "status_disagreement")
	if err := eng.ResolveConflict(ctx, dis.ID, "Ana", "refresh_now", "", ""); err == nil {
		t.Fatal("refresh_now must refuse while PayPal still disagrees")
	}
	if err := eng.ResolveConflict(ctx, dis.ID, "Ana", "accept_and_close", "", ""); err != nil {
		t.Fatal(err)
	}
	it, _ = store.GetItem(ctx, st.DB, dis.ItemID, false)
	if it.NeedsReview {
		t.Fatal("accept_and_close should clear the review flag")
	}

	// Every resolution above is in the audit log with the person's name, and the chain is intact.
	var n int
	_ = st.DB.QueryRow(`SELECT count(*) FROM audit_log WHERE event='conflict_resolved' AND actor='Ana'`).Scan(&n)
	if n != 4 {
		t.Fatalf("expected 4 audited resolutions, got %d", n)
	}
	if _, broken, err := store.VerifyAudit(ctx, st.DB); err != nil || broken != 0 {
		t.Fatalf("audit chain broken at %d (%v)", broken, err)
	}
	if d := eng.Fake.Duplicates(); len(d) != 0 {
		t.Fatalf("resolutions caused duplicate payments: %v", d)
	}
}
