package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port       int
	LogLevel   string
	AdminToken string
	APIKey     []string
	RateLimit  int

	// TrustProxyHeaders enables honoring X-Forwarded-For / X-Real-IP when
	// deriving the client IP (rate limiting, logs, request history). Enable
	// only behind a reverse proxy that overwrites these headers; otherwise
	// they are client-controlled and spoofable.
	TrustProxyHeaders bool

	// VPN controls the embedded VPNGate provider (single-binary mode).
	// When enabled, freegate starts an in-process OpenVPN tunnel + SOCKS5
	// per-OS (linux/darwin/windows) and routes upstreams via 127.0.0.1:9050.
	// When disabled, upstreams go direct (Dialer.IsDirect).
	VPNEnabled  bool
	VPNProvider string // auto|vpngate|direct

	// VPNGate keeps an OpenVPN tunnel to a VPNGate relay server via the
	// in-process supervisor, exposing SOCKS5 through it for all upstream
	// traffic.
	VPNGateSocksPort      int // SOCKS5 port used for all upstream traffic
	// VPNGateCountry filters the relay list by country: a country name
	// substring ("Japan") or ISO code ("JP"), prefix with "!" to exclude
	// ("!US"). Empty = all countries.
	VPNGateCountry string
	// VPNGateMinScore / VPNGateMaxPing filter the relay list by server
	// score and ping (ms). Zero disables the filter.
	VPNGateMinScore int
	VPNGateMaxPing  int
	// VPNGateRefreshSeconds bounds how often the VPNGate server list is
	// re-fetched (default 300).
	VPNGateRefreshSeconds int

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

	SOCKSAddr string

	ProvidersDBPath string
}

// IsDirect reports whether upstreams should bypass the VPN tunnel.
func (c *Config) IsDirect() bool { return c.SOCKSAddr == "" }

// IsAdminAuthEnabled reports whether admin auth is configured.
func (c *Config) IsAdminAuthEnabled() bool { return c.AdminToken != "" }

func Load() *Config {
	cfg := &Config{
		Port:       envInt("PORT", 1234),
		LogLevel:   envStr("LOG_LEVEL", "info"),
		AdminToken: envStr("ADMIN_TOKEN", ""),
		APIKey:     envSlice("API_KEY", ""),
		RateLimit:  envInt("RATE_LIMIT", 60),

		TrustProxyHeaders: envBool("TRUST_PROXY_HEADERS", false),

		VPNEnabled:  envBool("VPN_ENABLED", true),
		VPNProvider: envStr("VPN_PROVIDER", "auto"),

		VPNGateSocksPort:      envInt("VPNGATE_SOCKS_PORT", 9050),
		VPNGateCountry:        envStr("VPNGATE_COUNTRY", ""),
		VPNGateMinScore:       envInt("VPNGATE_MIN_SCORE", 0),
		VPNGateMaxPing:        envInt("VPNGATE_MAX_PING", 0),
		VPNGateRefreshSeconds: envInt("VPNGATE_REFRESH_SECONDS", 300),

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

	// In-process SOCKS on 127.0.0.1:9050 when VPN enabled, direct otherwise.
	if !cfg.VPNEnabled || cfg.VPNProvider == "direct" {
		cfg.SOCKSAddr = ""
	} else {
		cfg.SOCKSAddr = "127.0.0.1:" + strconv.Itoa(cfg.VPNGateSocksPort)
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
	for _, k := range c.APIKey {
		if strings.TrimSpace(k) == "" {
			errs = append(errs, "API_KEY entries must be non-empty")
			break
		}
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
	if c.VPNEnabled && c.SOCKSAddr == "" && c.VPNProvider != "direct" {
		errs = append(errs, "SOCKSAddr must be set when VPN_ENABLED is true")
	}
	if c.VPNProvider != "auto" && c.VPNProvider != "vpngate" && c.VPNProvider != "direct" {
		errs = append(errs, fmt.Sprintf("VPN_PROVIDER must be auto, vpngate or direct, got %q", c.VPNProvider))
	}
	if c.Port <= 0 || c.Port > 65535 {
		errs = append(errs, fmt.Sprintf("PORT must be between 1 and 65535, got %d", c.Port))
	}
	// VPN ports — only validate when VPN is enabled.
	if c.VPNEnabled {
		if c.VPNGateSocksPort <= 0 || c.VPNGateSocksPort > 65535 {
			errs = append(errs, fmt.Sprintf("VPNGATE_SOCKS_PORT must be between 1 and 65535, got %d", c.VPNGateSocksPort))
		}
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
