package config

import "testing"

func TestRefusesNonSandbox(t *testing.T) {
	c := Config{PayPalBaseURL: "https://api-m.paypal.com", DatabaseURL: "x"}
	if err := c.Validate(); err == nil {
		t.Fatal("live URL must be refused")
	}
	c.PayPalBaseURL = SandboxBaseURL
	if err := c.Validate(); err != nil {
		t.Fatalf("sandbox URL must pass: %v", err)
	}
}

func TestProductionNeedsToken(t *testing.T) {
	c := Config{Env: "production", PayPalBaseURL: SandboxBaseURL, DatabaseURL: "x"}
	if c.Validate() == nil {
		t.Fatal("production without ADMIN_TOKEN must fail")
	}
}
