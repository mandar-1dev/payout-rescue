// Command check verifies every connection the app needs and exits non-zero on failure.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mandar-1dev/payoutrescue/internal/ai"
	"github.com/mandar-1dev/payoutrescue/internal/config"
	"github.com/mandar-1dev/payoutrescue/internal/paypal"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	failed := false
	report := func(name string, err error, detail string) {
		if err != nil {
			failed = true
			fmt.Printf("FAIL  %-22s %v\n", name, err)
			return
		}
		fmt.Printf("ok    %-22s %s\n", name, detail)
	}
	cfg, err := config.Load()
	report("configuration", err, "sandbox-only checks passed")
	if err != nil {
		os.Exit(1)
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	report("database", err, "reachable")
	if err == nil {
		report("migrations", st.Migrate(ctx), "applied")
		st.Close()
	}
	if cfg.LiveConfigured() {
		c := paypal.NewHTTPClient(cfg.PayPalBaseURL, cfg.PayPalClientID, cfg.PayPalClientSecret)
		tok, err := c.AccessToken(ctx)
		d := ""
		if err == nil {
			d = "token " + tok[:min(8, len(tok))] + "... (sandbox)"
		}
		report("paypal sandbox", err, d)
	} else {
		fmt.Println("skip  paypal sandbox         PAYPAL_CLIENT_ID / PAYPAL_CLIENT_SECRET not set (FAKE mode only)")
	}
	if cfg.PayPalWebhookID == "" {
		fmt.Println("skip  webhook id             PAYPAL_WEBHOOK_ID not set (signatures cannot be verified)")
	}
	if p := ai.NewProvider(cfg.LLMProvider, cfg.LLMAPIKey, cfg.LLMModel); p != nil {
		ex := ai.Extract(ctx, p, "Hi, please use my email check.me@example.com")
		var e error
		if ex.Source != "model" || ex.NewEmail != "check.me@example.com" {
			e = fmt.Errorf("model did not return a valid extraction (source=%s notes=%v)", ex.Source, ex.Notes)
		}
		report("llm", e, "schema-valid JSON from "+cfg.LLMModel)
	} else {
		fmt.Println("skip  llm                    no provider configured (templates and deterministic extraction are used)")
	}
	if failed {
		os.Exit(1)
	}
}
