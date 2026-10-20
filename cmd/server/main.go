// Command server runs PayoutRescue: API, webhook receiver, workers and the embedded UI.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mandar-1dev/payoutrescue/internal/ai"
	"github.com/mandar-1dev/payoutrescue/internal/config"
	"github.com/mandar-1dev/payoutrescue/internal/engine"
	"github.com/mandar-1dev/payoutrescue/internal/fakepaypal"
	"github.com/mandar-1dev/payoutrescue/internal/httpapi"
	"github.com/mandar-1dev/payoutrescue/internal/paypal"
	"github.com/mandar-1dev/payoutrescue/internal/sse"
	"github.com/mandar-1dev/payoutrescue/internal/store"
	"github.com/mandar-1dev/payoutrescue/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)
	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration error", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	if cfg.AutoMigrate {
		if err := st.Migrate(ctx); err != nil {
			log.Error("migration failed", "err", err)
			os.Exit(1)
		}
	}

	var live paypal.Client
	if cfg.LiveConfigured() {
		live = paypal.NewHTTPClient(cfg.PayPalBaseURL, cfg.PayPalClientID, cfg.PayPalClientSecret)
	} else {
		log.Warn("PayPal sandbox credentials not set: only FAKE mode is available")
	}
	var fake *fakepaypal.Fake
	if cfg.FakeEnabled {
		fake = fakepaypal.New()
	}
	if cfg.WebhookBypass {
		log.Warn("WEBHOOK_BYPASS is on: webhook signatures are NOT verified (sandbox demos only)")
	}
	hub := sse.New()
	eng := &engine.Engine{Store: st, Live: live, Fake: fake, AI: ai.NewProvider(cfg.LLMProvider, cfg.LLMAPIKey, cfg.LLMModel),
		Platform: cfg.Platform, ReplyTo: "payouts@example.com", WebhookBypass: cfg.WebhookBypass, WebhookID: cfg.PayPalWebhookID, Log: log, Notify: hub.Publish}
	if n, err := eng.RehydrateFake(ctx); err != nil {
		log.Warn("could not restore fake items", "err", err)
	} else if n > 0 {
		log.Info("restored fake items", "count", n)
	}

	srv := &httpapi.Server{Eng: eng, Cfg: cfg, Hub: hub, Static: web.Dist(), Live: live, Log: log}
	httpSrv := &http.Server{Addr: ":" + cfg.Port, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}

	go func() { // reconciler
		t := time.NewTimer(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				s := eng.Reconcile(rctx)
				cancel()
				if len(s.Errors) > 0 {
					log.Warn("reconcile finished with errors", "errors", s.Errors)
				}
				hub.Publish("reconciled", s)
				t.Reset(cfg.ReconcileInterval)
			}
		}
	}()

	go func() {
		log.Info("listening", "addr", httpSrv.Addr, "env", cfg.Env, "live_configured", cfg.LiveConfigured(), "fake", cfg.FakeEnabled)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(sctx)
}
