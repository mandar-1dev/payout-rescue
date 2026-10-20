// Package config loads and validates environment configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// SandboxBaseURL is the only PayPal base URL this application will talk to.
const SandboxBaseURL = "https://api-m.sandbox.paypal.com"

type Config struct {
	Env                string
	Port               string
	DatabaseURL        string
	AutoMigrate        bool
	AdminToken         string
	PublicBaseURL      string
	PayPalBaseURL      string
	PayPalClientID     string
	PayPalClientSecret string
	PayPalWebhookID    string
	WebhookBypass      bool // sandbox-only: skip signature verification (simulator events)
	FakeEnabled        bool
	LLMProvider        string
	LLMAPIKey          string
	LLMModel           string
	ReconcileInterval  time.Duration
	Platform           string
}

func getenv(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func getbool(k string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// Load reads configuration and refuses anything that is not sandbox-safe.
func Load() (Config, error) {
	c := Config{
		Env:                getenv("APP_ENV", "development"),
		Port:               getenv("PORT", "8080"),
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		AutoMigrate:        getbool("AUTO_MIGRATE", true),
		AdminToken:         os.Getenv("ADMIN_TOKEN"),
		PublicBaseURL:      strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		PayPalBaseURL:      strings.TrimRight(getenv("PAYPAL_BASE_URL", SandboxBaseURL), "/"),
		PayPalClientID:     os.Getenv("PAYPAL_CLIENT_ID"),
		PayPalClientSecret: os.Getenv("PAYPAL_CLIENT_SECRET"),
		PayPalWebhookID:    os.Getenv("PAYPAL_WEBHOOK_ID"),
		WebhookBypass:      getbool("WEBHOOK_BYPASS", false),
		FakeEnabled:        getbool("FAKE_ENABLED", true),
		LLMProvider:        strings.ToLower(getenv("LLM_PROVIDER", "none")),
		LLMAPIKey:          os.Getenv("LLM_API_KEY"),
		LLMModel:           getenv("LLM_MODEL", "claude-haiku-5-5"),
		Platform:           getenv("PLATFORM_NAME", "Acme Creators"),
	}
	secs, err := strconv.Atoi(getenv("RECONCILE_INTERVAL_SECONDS", "300"))
	if err != nil || secs < 10 {
		return c, errors.New("RECONCILE_INTERVAL_SECONDS must be an integer >= 10")
	}
	c.ReconcileInterval = time.Duration(secs) * time.Second
	return c, c.Validate()
}

// Validate enforces the hard safety rules.
func (c Config) Validate() error {
	if c.PayPalBaseURL != SandboxBaseURL {
		return fmt.Errorf("refusing to start: PAYPAL_BASE_URL must be %s (sandbox only), got %q", SandboxBaseURL, c.PayPalBaseURL)
	}
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	if c.AdminToken == "" && c.Env == "production" {
		return errors.New("ADMIN_TOKEN is required in production")
	}
	return nil
}

// LiveConfigured reports whether PayPal sandbox credentials are present.
func (c Config) LiveConfigured() bool { return c.PayPalClientID != "" && c.PayPalClientSecret != "" }
