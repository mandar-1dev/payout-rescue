package demo_test

import (
	"context"
	"os"
	"testing"

	"github.com/mandar-1dev/payoutrescue/internal/demo"
	"github.com/mandar-1dev/payoutrescue/internal/engine"
	"github.com/mandar-1dev/payoutrescue/internal/fakepaypal"
	"github.com/mandar-1dev/payoutrescue/internal/state"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

func TestSeedPacksAndAssertSevenConflicts(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Truncate tables for a clean slate
	if _, err := st.DB.ExecContext(ctx, `TRUNCATE conflicts, notes, messages, actions, webhook_events, audit_log, payout_items, payout_batches, logical_payouts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	fk := fakepaypal.New()
	eng := &engine.Engine{Store: st, Fake: fk, Platform: "TestPlatform"}

	// 1. Seed standard pack
	var stdRecs []engine.Recipient
	for _, d := range demo.Standard() {
		stdRecs = append(stdRecs, engine.Recipient{
			Name:        d.Name,
			Email:       d.Email,
			Amount:      d.Amount,
			Currency:    d.Currency,
			Purpose:     d.Purpose,
			FakeOutcome: d.FakeOutcome,
		})
	}
	if _, _, err := eng.CreateBatch(ctx, engine.CreateBatchInput{Mode: state.ModeFake, Recipients: stdRecs}); err != nil {
		t.Fatalf("seeding standard pack: %v", err)
	}

	// 2. Seed conflict pack
	if err := eng.SeedConflicts(ctx); err != nil {
		t.Fatalf("seeding conflict pack: %v", err)
	}

	// 3. Assert exactly 7 open conflicts
	openConflicts, err := store.ListConflicts(ctx, st.DB, "open", 100)
	if err != nil {
		t.Fatalf("listing open conflicts: %v", err)
	}
	if len(openConflicts) != 7 {
		t.Fatalf("expected exactly 7 open conflicts, got %d", len(openConflicts))
	}

	// Verify each of the 7 conflict kinds is present
	kinds := map[string]bool{}
	for _, c := range openConflicts {
		kinds[c.Kind] = true
	}
	expectedKinds := []string{
		engine.ConflictClaimRace,
		engine.ConflictAmbiguousReply,
		engine.ConflictEmailSameAsOnFile,
		engine.ConflictRefusalVsProposal,
		engine.ConflictHeldWithCorrection,
		engine.ConflictAttemptsExhausted,
		engine.ConflictStatusDisagreement,
	}
	for _, k := range expectedKinds {
		if !kinds[k] {
			t.Errorf("expected open conflict of kind %q not found", k)
		}
	}
}
