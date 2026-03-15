package config

import (
	"testing"
)

func setEnvs(t *testing.T, kvs map[string]string) {
	t.Helper()
	for k, v := range kvs {
		t.Setenv(k, v)
	}
}

func TestEnvStr_ReturnsEnvValue(t *testing.T) {
	t.Setenv("TEST_STR_KEY", "hello")
	if got := envStr("TEST_STR_KEY", "default"); got != "hello" {
		t.Errorf("envStr() = %q, want %q", got, "hello")
	}
}

func TestEnvStr_ReturnsFallback(t *testing.T) {
	t.Setenv("TEST_STR_MISSING", "")
	if got := envStr("TEST_STR_MISSING", "fallback"); got != "fallback" {
		t.Errorf("envStr() = %q, want %q", got, "fallback")
	}
}

func TestEnvInt_ReturnsEnvValue(t *testing.T) {
	t.Setenv("TEST_INT_KEY", "42")
	if got := envInt("TEST_INT_KEY", 0); got != 42 {
		t.Errorf("envInt() = %d, want %d", got, 42)
	}
}

func TestEnvInt_ReturnsFallbackOnMissing(t *testing.T) {
	t.Setenv("TEST_INT_MISSING", "")
	if got := envInt("TEST_INT_MISSING", 99); got != 99 {
		t.Errorf("envInt() = %d, want %d", got, 99)
	}
}

func TestEnvInt_ReturnsFallbackOnInvalid(t *testing.T) {
	t.Setenv("TEST_INT_BAD", "not_a_number")
	if got := envInt("TEST_INT_BAD", 77); got != 77 {
		t.Errorf("envInt() = %d, want %d", got, 77)
	}
}

func TestSplitCSV(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty string", "", nil},
		{"single value", "alpha", []string{"alpha"}},
		{"multiple values", "a,b,c", []string{"a", "b", "c"}},
		{"whitespace trimmed", " a , b , c ", []string{"a", "b", "c"}},
		{"empty segments dropped", "a,,b,,,c", []string{"a", "b", "c"}},
		{"all empty", ",,,", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitCSV(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("splitCSV(%q) len = %d, want %d", tt.in, len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitCSV(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestLoadFromEnv_Defaults(t *testing.T) {
	for _, key := range []string{
		"LISTEN_ADDR", "SERVICE_TOKEN", "UPSTREAM_URL",
		"RATE_LIMIT_RPS", "RATE_LIMIT_BURST",
		"PROXY_TIMEOUT_SEC", "MAX_BODY_SIZE", "RATE_LIMIT_EXEMPT",
	} {
		t.Setenv(key, "")
	}

	cfg := LoadFromEnv()

	if cfg.ListenAddr != ":8150" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8150")
	}
	if cfg.ServiceToken != "" {
		t.Errorf("ServiceToken = %q, want empty", cfg.ServiceToken)
	}
	if cfg.UpstreamURL != "http://phorge:80" {
		t.Errorf("UpstreamURL = %q, want %q", cfg.UpstreamURL, "http://phorge:80")
	}
	if cfg.RateLimitRPS != 0 {
		t.Errorf("RateLimitRPS = %d, want 0", cfg.RateLimitRPS)
	}
	if cfg.RateLimitBurst != 20 {
		t.Errorf("RateLimitBurst = %d, want 20", cfg.RateLimitBurst)
	}
	if cfg.ProxyTimeoutSec != 30 {
		t.Errorf("ProxyTimeoutSec = %d, want 30", cfg.ProxyTimeoutSec)
	}
	if cfg.MaxBodySize != "10M" {
		t.Errorf("MaxBodySize = %q, want %q", cfg.MaxBodySize, "10M")
	}
	if len(cfg.RateLimitExempt) != 2 {
		t.Fatalf("RateLimitExempt len = %d, want 2", len(cfg.RateLimitExempt))
	}
}

func TestLoadFromEnv_CustomValues(t *testing.T) {
	setEnvs(t, map[string]string{
		"LISTEN_ADDR":       ":9999",
		"SERVICE_TOKEN":     "s3cret",
		"UPSTREAM_URL":      "http://localhost:3000",
		"RATE_LIMIT_RPS":    "100",
		"RATE_LIMIT_BURST":  "50",
		"PROXY_TIMEOUT_SEC": "60",
		"MAX_BODY_SIZE":     "20M",
		"RATE_LIMIT_EXEMPT": "ping,pong",
	})

	cfg := LoadFromEnv()

	if cfg.ListenAddr != ":9999" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9999")
	}
	if cfg.ServiceToken != "s3cret" {
		t.Errorf("ServiceToken = %q, want %q", cfg.ServiceToken, "s3cret")
	}
	if cfg.UpstreamURL != "http://localhost:3000" {
		t.Errorf("UpstreamURL = %q, want %q", cfg.UpstreamURL, "http://localhost:3000")
	}
	if cfg.RateLimitRPS != 100 {
		t.Errorf("RateLimitRPS = %d, want 100", cfg.RateLimitRPS)
	}
	if cfg.RateLimitBurst != 50 {
		t.Errorf("RateLimitBurst = %d, want 50", cfg.RateLimitBurst)
	}
	if cfg.ProxyTimeoutSec != 60 {
		t.Errorf("ProxyTimeoutSec = %d, want 60", cfg.ProxyTimeoutSec)
	}
	if cfg.MaxBodySize != "20M" {
		t.Errorf("MaxBodySize = %q, want %q", cfg.MaxBodySize, "20M")
	}
	if len(cfg.RateLimitExempt) != 2 || cfg.RateLimitExempt[0] != "ping" || cfg.RateLimitExempt[1] != "pong" {
		t.Errorf("RateLimitExempt = %v, want [ping pong]", cfg.RateLimitExempt)
	}
}
