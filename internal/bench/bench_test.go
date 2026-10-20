package bench

import (
	"context"
	"os"
	"testing"
)

func TestBenchRuns(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	res, err := Run(context.Background(), Options{DatabaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Summary
	if s.Scenarios < 30 {
		t.Fatalf("need at least 30 scenarios, got %d", s.Scenarios)
	}
	for _, r := range res.Scenarios {
		if !r.Pass {
			t.Errorf("scenario %s %q failed: pr=%+v naive=%+v", r.ID, r.Name, r.PayoutRescue, r.Naive)
		}
	}
	if s.PRDuplicates != 0 {
		t.Fatalf("PayoutRescue must never double-pay, got %d", s.PRDuplicates)
	}
	if s.NaiveDuplicates == 0 {
		t.Fatalf("the naive script should double-pay in at least one scenario (otherwise the comparison proves nothing)")
	}
	if s.ExtractionTotal != 20 {
		t.Fatalf("extraction cases: %d", s.ExtractionTotal)
	}
	t.Logf("scenarios=%d passed=%d naiveDup=%d prDup=%d naiveUnsafe=%d naiveUnnoticed=%d extraction=%d/%d steps naive=%d pr=%d",
		s.Scenarios, s.Passed, s.NaiveDuplicates, s.PRDuplicates, s.NaiveUnsafeRetries, s.NaiveUnnoticed, s.ExtractionCorrect, s.ExtractionTotal, s.NaiveSteps, s.PRSteps)
}
