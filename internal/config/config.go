package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port       int
	LogLevel   string
	AdminToken string
	RateLimit  int

	// TrustProxyHeaders enables honoring X-Forwarded-For / X-Real-IP when
	// deriving the client IP (rate limiting, logs, request history). Enable
	// only behind a reverse proxy that overwrites these headers; otherwise
	// they are client-controlled and spoofable.
	TrustProxyHeaders bool

	UpstreamURLOpenCode           string
	UpstreamKeyOpenCode           []string
	UpstreamOpenCodeFreeAllowlist []string

	UpstreamURLKilo string
	UpstreamKeyKilo string

	UpstreamURLLLM7 string

	UpstreamDefault string

	// UpstreamCapture is the master switch for raw upstream response
	// logging via slog (stdout). It defaults to false: captures contain
	// full conversation content and would otherwise flood the log.
	UpstreamCapture bool

	UpstreamRefreshOpenCode int
	UpstreamRefreshKilo     int
	UpstreamRefreshLLM7     int

	// ResponseModels is a direct config for models that use the OpenAI Responses API
	// (e.g. muse-spark). Comma-separated substrings matched case-insensitively
	// against the model ID. If empty, defaults to muse-spark.
	ResponseModels []string

	// MessageModels is a direct config for models served by the Anthropic
	// Messages API on the OpenCode Zen gateway (e.g. union-alpha per 9router
	// PR #4111). Substrings matched case-insensitively. Defaults to union-alpha.
	MessageModels []string

	ProvidersDBPath string
}

func Load() *Config {
	cfg := &Config{
		Port:       envInt("PORT", 1234),
		LogLevel:   envStr("LOG_LEVEL", "info"),
		AdminToken: envStr("ADMIN_TOKEN", ""),
		RateLimit:  envInt("RATE_LIMIT", 60),

		TrustProxyHeaders: envBool("TRUST_PROXY_HEADERS", false),

		UpstreamURLOpenCode:           envStr("UPSTREAM_URL_OPENCODE", "https://opencode.ai/zen/v1"),
		UpstreamKeyOpenCode:           envSlice("UPSTREAM_KEY_OPENCODE", "public"),
		UpstreamOpenCodeFreeAllowlist: envSlice("UPSTREAM_OPENCODE_FREE_ALLOWLIST", "big-pickle"),

		UpstreamURLKilo: envStr("UPSTREAM_URL_KILO", "https://api.kilo.ai/api/openrouter"),
		UpstreamKeyKilo: envStr("UPSTREAM_KEY_KILO", "anonymous"),

		UpstreamURLLLM7: envStr("UPSTREAM_URL_LLM7", "https://api.llm7.io/v1"),

		UpstreamDefault: envStr("UPSTREAM_DEFAULT", "opencode"),

		UpstreamCapture: envBool("UPSTREAM_CAPTURE", false),

		UpstreamRefreshOpenCode: envInt("UPSTREAM_REFRESH_OPENCODE", 60),
		UpstreamRefreshKilo:     envInt("UPSTREAM_REFRESH_KILO", 60),
		UpstreamRefreshLLM7:     envInt("UPSTREAM_REFRESH_LLM7", 300),

		ResponseModels: envSlice("RESPONSE_MODELS", "muse-spark,muse_spark"),
		MessageModels:  envSlice("MESSAGE_MODELS", "union-alpha"),

		ProvidersDBPath: envStr("PROVIDERS_DB_PATH", "./data/providers.db"),
	}

	// API_KEY was removed in favour of DB-managed client keys. Warn loudly so
	// an operator upgrading with a stale .env does not silently lose /v1/*
	// access — the server would start fine and every request would 401.
	if os.Getenv("API_KEY") != "" {
		log.Printf("warn: API_KEY is no longer supported; create client keys at /settings or POST /api/api-keys")
	}

	return cfg
}

func (c *Config) Validate() error {
	var errs []string

	if c.AdminToken == "" {
		errs = append(errs, "ADMIN_TOKEN is required")
	} else if len(c.AdminToken) < 6 {
		errs = append(errs, "ADMIN_TOKEN must be at least 6 characters")
	}

	if c.UpstreamURLOpenCode == "" {
		errs = append(errs, "UPSTREAM_URL_OPENCODE is required")
	}
	if c.UpstreamURLKilo == "" {
		errs = append(errs, "UPSTREAM_URL_KILO is required")
	}
	if c.UpstreamURLLLM7 == "" {
		errs = append(errs, "UPSTREAM_URL_LLM7 is required")
	}
	if c.Port <= 0 || c.Port > 65535 {
		errs = append(errs, fmt.Sprintf("PORT must be between 1 and 65535, got %d", c.Port))
	}
	if c.RateLimit <= 0 {
		errs = append(errs, "RATE_LIMIT must be positive")
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envSlice(key, def string) []string {
	v := os.Getenv(key)
	if v == "" {
		v = def
	}
	var result []string
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return def
	}
}
