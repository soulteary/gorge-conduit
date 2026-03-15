package config

import (
	"log"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddr   string
	ServiceToken string

	// Upstream Phorge PHP app URL (e.g. http://phorge:80)
	UpstreamURL string

	// Rate limiting
	RateLimitRPS   int // requests per second per client, 0 = disabled
	RateLimitBurst int // burst size

	// Proxy behaviour
	ProxyTimeoutSec int
	MaxBodySize     string // Echo body limit, e.g. "10M"

	// Methods that bypass rate limiting (comma-separated)
	RateLimitExempt []string
}

func LoadFromEnv() *Config {
	return &Config{
		ListenAddr:      envStr("LISTEN_ADDR", ":8150"),
		ServiceToken:    envStr("SERVICE_TOKEN", ""),
		UpstreamURL:     envStr("UPSTREAM_URL", "http://phorge:80"),
		RateLimitRPS:    envInt("RATE_LIMIT_RPS", 0),
		RateLimitBurst:  envInt("RATE_LIMIT_BURST", 20),
		ProxyTimeoutSec: envInt("PROXY_TIMEOUT_SEC", 30),
		MaxBodySize:     envStr("MAX_BODY_SIZE", "10M"),
		RateLimitExempt: splitCSV(envStr("RATE_LIMIT_EXEMPT", "conduit.ping,conduit.getcapabilities")),
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
		log.Printf("[config] WARNING: invalid integer for %s=%q, using default %d", key, v, fallback)
	}
	return fallback
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
