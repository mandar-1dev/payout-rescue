package engine_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/mandar-1dev/payoutrescue/internal/engine"
	"github.com/mandar-1dev/payoutrescue/internal/fakepaypal"
	"github.com/mandar-1dev/payoutrescue/internal/state"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

func setupEngineTest(t *testing.T) (*engine.Engine, *store.Store, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, `TRUNCATE conflicts, notes, messages, actions, webhook_events, audit_log, payout_items, payout_batches, logical_payouts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	fk := fakepaypal.New()
	eng := &engine.Engine{Store: st, Fake: fk, Platform: "TestPlatform"}
	return eng, st, ctx
}

func TestConflictDedupe(t *testing.T) {
	eng, st, ctx := setupEngineTest(t)

	_, items, err := eng.CreateBatch(ctx, engine.CreateBatchInput{
		Mode: state.ModeFake,
		Recipients: []engine.Recipient{
			{Name: "Dedupe User", Email: "dedupe@example.com", Amount: "10.00"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	itemID := items[0].ID

	// Call OpenConflict multiple times with the exact same dedupe_key
	dedupeKey := "test_dedupe_123"
	err = st.Tx(ctx, func(tx *sql.Tx) error {
		// First insert should succeed
		inserted, err := eng.OpenConflict(ctx, tx, itemID, engine.ConflictClaimRace, "First open", nil, dedupeKey)
		if err != nil || !inserted {
			t.Fatalf("first OpenConflict failed: inserted=%v, err=%v", inserted, err)
		}
		// Second insert with same key should be deduped (inserted = false, no error)
		inserted2, err := eng.OpenConflict(ctx, tx, itemID, engine.ConflictClaimRace, "Second open", nil, dedupeKey)
		if err != nil || inserted2 {
			t.Fatalf("second OpenConflict should be deduped: inserted=%v, err=%v", inserted2, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	conflicts, err := store.ListConflictsByItem(ctx, st.DB, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 conflict due to dedupe, got %d", len(conflicts))
	}
}

func TestDisallowedResolutionsRefused(t *testing.T) {
	eng, st, ctx := setupEngineTest(t)

	_, items, err := eng.CreateBatch(ctx, engine.CreateBatchInput{
		Mode: state.ModeFake,
		Recipients: []engine.Recipient{
			{Name: "Resolutions User", Email: "res@example.com", Amount: "20.00"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	itemID := items[0].ID

	// Create a claim_race conflict (allowed: acknowledge)
	err = st.Tx(ctx, func(tx *sql.Tx) error {
		_, err := eng.OpenConflict(ctx, tx, itemID, engine.ConflictClaimRace, "Claim race test", nil, "race_key_1")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	conflicts, _ := store.ListConflictsByItem(ctx, st.DB, itemID)
	if len(conflicts) == 0 {
		t.Fatal("conflict was not created")
	}
	cid := conflicts[0].ID

	// 1. Missing approver must fail
	if err := eng.ResolveConflict(ctx, cid, "", "acknowledge", "", ""); err == nil {
		t.Error("expected error when approver is empty")
	}

	// 2. Disallowed resolution for claim_race (e.g., 'use_email' or 'reissue' or 'dismiss')
	if err := eng.ResolveConflict(ctx, cid, "Alice", "use_email", "", ""); err == nil {
		t.Error("expected error for disallowed resolution 'use_email' on claim_race")
	}
	if err := eng.ResolveConflict(ctx, cid, "Alice", "invalid_random_res", "", ""); err == nil {
		t.Error("expected error for random disallowed resolution")
	}

	// 3. Allowed resolution succeeds
	if err := eng.ResolveConflict(ctx, cid, "Alice", "acknowledge", "", "noted"); err != nil {
		t.Fatalf("expected allowed resolution to succeed, got %v", err)
	}

	// 4. Resolving an already resolved conflict must fail
	if err := eng.ResolveConflict(ctx, cid, "Alice", "acknowledge", "", "again"); err == nil {
		t.Error("expected error resolving already resolved conflict")
	}
}

func TestResolutionsNeverMoveMoneyDirectly(t *testing.T) {
	eng, st, ctx := setupEngineTest(t)

	_, items, err := eng.CreateBatch(ctx, engine.CreateBatchInput{
		Mode: state.ModeFake,
		Recipients: []engine.Recipient{
			{Name: "Safety User", Email: "safety@example.com", Amount: "50.00"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// CreateBatch returns rows as first inserted; read the real status back before comparing.
	it, err := store.GetItem(ctx, st.DB, items[0].ID, false)
	if err != nil {
		t.Fatal(err)
	}
	origStatus := it.Status

	err = st.Tx(ctx, func(tx *sql.Tx) error {
		_, err := eng.OpenConflict(ctx, tx, it.ID, engine.ConflictAmbiguousReply, "Ambiguous test", map[string]any{"candidates": []string{"alt1@example.com", "alt2@example.com"}}, "ambig_safety")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	confls, _ := store.ListConflictsByItem(ctx, st.DB, it.ID)
	cid := confls[0].ID

	// Resolve with use_email
	if err := eng.ResolveConflict(ctx, cid, "Bob", "use_email", "alt1@example.com", ""); err != nil {
		t.Fatalf("resolve with use_email failed: %v", err)
	}

	// Item status must remain unchanged by the conflict resolution itself!
	after, err := store.GetItem(ctx, st.DB, it.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != origStatus {
		t.Fatalf("conflict resolution altered status from %v to %v; resolutions must never directly change payout status!", origStatus, after.Status)
	}
	if after.CorrectedEmail != "alt1@example.com" {
		t.Fatalf("expected corrected email 'alt1@example.com', got %q", after.CorrectedEmail)
	}
}

func TestStaleProposalsSuperseded(t *testing.T) {
	eng, st, ctx := setupEngineTest(t)

	_, items, err := eng.CreateBatch(ctx, engine.CreateBatchInput{
		Mode: state.ModeFake,
		Recipients: []engine.Recipient{
			{Name: "Stale User", Email: "stale@example.com", Amount: "95.00"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	it := items[0]

	// Setting a corrected email proposes a cancel action for an unclaimed item
	if err := eng.SetCorrectedEmail(ctx, it.ID, "stale.alt@example.com", "operator"); err != nil {
		t.Fatal(err)
	}

	actions, err := store.ListActions(ctx, st.DB, "proposed", it.ID, 10)
	if err != nil || len(actions) == 0 {
		t.Fatalf("expected proposed cancel action, got %v (err: %v)", actions, err)
	}

	// Now recipient claims the payment -> status becomes SUCCESS
	if err := store.UpdateItemStatus(ctx, st.DB, it.ID, state.Success, ""); err != nil {
		t.Fatal(err)
	}

	// Re-evaluate: Evaluate should supersede stale cancel proposal and open claim_race
	if err := eng.Evaluate(ctx, it.ID); err != nil {
		t.Fatal(err)
	}

	// Proposed cancel action should now be rejected as superseded
	actionAfter, err := store.GetAction(ctx, st.DB, actions[0].ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if actionAfter.Status != "rejected" {
		t.Fatalf("expected superseded action to be rejected, got status %q", actionAfter.Status)
	}

	// A claim_race conflict should have been opened
	confls, err := store.ListConflictsByItem(ctx, st.DB, it.ID)
	if err != nil || len(confls) == 0 {
		t.Fatalf("expected claim_race conflict opened after superseding, got %v (err: %v)", confls, err)
	}
	if confls[0].Kind != engine.ConflictClaimRace {
		t.Fatalf("expected conflict kind claim_race, got %q", confls[0].Kind)
	}
}
