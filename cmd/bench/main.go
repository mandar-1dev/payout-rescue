// Command bench runs the scripted benchmark and writes bench-results.json.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mandar-1dev/payoutrescue/internal/bench"
	"github.com/mandar-1dev/payoutrescue/internal/config"
	"github.com/mandar-1dev/payoutrescue/internal/paypal"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(1)
	}
	opt := bench.Options{DatabaseURL: url}
	if cfg, err := config.Load(); err == nil && cfg.LiveConfigured() {
		opt.Live = paypal.NewHTTPClient(cfg.PayPalBaseURL, cfg.PayPalClientID, cfg.PayPalClientSecret)
	}
	res, err := bench.Run(ctx, opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "benchmark failed:", err)
		os.Exit(1)
	}
	fmt.Printf("%-5s %-20s %-6s %-7s %s\n", "ID", "Group", "Label", "Result", "Scenario")
	for _, r := range res.Scenarios {
		status := "PASS"
		if !r.Pass {
			status = "FAIL"
		}
		fmt.Printf("%-5s %-20s %-6s %-7s %s\n", r.ID, trunc(r.Group, 20), strings.TrimSuffix(r.Label, "_SANDBOX"), status, r.Name)
	}
	s := res.Summary
	fmt.Printf("\n%d scenarios, %d passed (%v)\n", s.Scenarios, s.Passed, s.LabelCounts)
	fmt.Printf("duplicate payments    naive=%d  payoutrescue=%d  (%d comparison scenarios)\n", s.NaiveDuplicates, s.PRDuplicates, s.ComparisonScenarios)
	fmt.Printf("unsafe retries        naive=%d  payoutrescue=%d\n", s.NaiveUnsafeRetries, s.PRUnsafeRetries)
	fmt.Printf("unnoticed returns     naive=%d  payoutrescue=%d\n", s.NaiveUnnoticed, s.PRUnnoticed)
	fmt.Printf("operator steps        naive=%d (ASSUMED %d per exception)  payoutrescue=%d\n", s.NaiveSteps, s.StepBaselineAssumed, s.PRSteps)
	fmt.Printf("reply extraction      %d/%d on the built-in synthetic set (%s)\n", s.ExtractionCorrect, s.ExtractionTotal, s.ExtractionSource)
	if err := os.WriteFile("bench-results.json", res.JSON(), 0o644); err == nil {
		fmt.Println("wrote bench-results.json")
	}
	if st, err := store.Open(ctx, url); err == nil {
		defer st.Close()
		if st.Migrate(ctx) == nil {
			_ = store.SaveBench(ctx, st.DB, res.JSON())
		}
	}
	if s.PRDuplicates != 0 || s.Passed != s.Scenarios {
		os.Exit(1)
	}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
