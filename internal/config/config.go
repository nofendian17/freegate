package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port      int
	LogLevel  string
	APIKey    string
	RateLimit int

	// VPN controls the embedded VPNGate provider (single-binary mode).
	// When enabled, freegate starts an in-process OpenVPN tunnel + SOCKS5
	// per-OS (linux/darwin/windows) and routes upstreams via 127.0.0.1:9050.
	// When disabled, upstreams go direct (Dialer.IsDirect).
	VPNEnabled  bool
	VPNProvider string // auto|vpngate|direct

	// VPNGate replaces the old Tor proxy. The "vpn" sidecar container
	// (cmd/vpngate-supervisor) keeps an OpenVPN tunnel to a VPNGate relay
	// server, exposes a SOCKS5 proxy through it, and a small HTTP control
	// API used for IP rotation.
	// Deprecated: VPNGateHost/CtrlPort kept for compat with docker-compose;
	// new single-binary uses VPNEnabled + in-process SOCKS 127.0.0.1:9050.
	VPNGateHost           string // SOCKS5 + control host (the "vpn" compose service)
	VPNGateSocksPort      int    // SOCKS5 port used for all upstream traffic
	VPNGateCtrlPort       int    // control API port (POST /rotate, GET /ip)
	VPNGateRotateInterval int    // minimum seconds between scheduled IP rotations

	UpstreamURLOpenCode           string
	UpstreamKeyOpenCode           []string
	UpstreamOpenCodeFreeAllowlist []string

	UpstreamURLKilo string
	UpstreamKeyKilo string

	UpstreamURLLLM7 string

	UpstreamDefault string

	UpstreamRefreshOpenCode int
	UpstreamRefreshKilo     int
	UpstreamRefreshLLM7     int

	SOCKSAddr string
}

func Load() *Config {
	cfg := &Config{
		Port:      envInt("PORT", 1234),
		LogLevel:  envStr("LOG_LEVEL", "info"),
		APIKey:    envStr("API_KEY", ""),
		RateLimit: envInt("RATE_LIMIT", 60),

		VPNEnabled:  envBool("VPN_ENABLED", true),
		VPNProvider: envStr("VPN_PROVIDER", "auto"),

		VPNGateHost:           envStr("VPNGATE_HOST", "127.0.0.1"),
		VPNGateSocksPort:      envInt("VPNGATE_SOCKS_PORT", 9050),
		VPNGateCtrlPort:       envInt("VPNGATE_CTRL_PORT", 8080),
		VPNGateRotateInterval: envInt("VPNGATE_ROTATE_INTERVAL", 30),

		UpstreamURLOpenCode:           envStr("UPSTREAM_URL_OPENCODE", "https://opencode.ai/zen/v1"),
		UpstreamKeyOpenCode:           envSlice("UPSTREAM_KEY_OPENCODE", "public"),
		UpstreamOpenCodeFreeAllowlist: envSlice("UPSTREAM_OPENCODE_FREE_ALLOWLIST", "big-pickle"),

		UpstreamURLKilo: envStr("UPSTREAM_URL_KILO", "https://api.kilo.ai/api/openrouter"),
		UpstreamKeyKilo: envStr("UPSTREAM_KEY_KILO", "anonymous"),

		UpstreamURLLLM7: envStr("UPSTREAM_URL_LLM7", "https://api.llm7.io/v1"),

		UpstreamDefault: envStr("UPSTREAM_DEFAULT", "opencode"),

		UpstreamRefreshOpenCode: envInt("UPSTREAM_REFRESH_OPENCODE", 60),
		UpstreamRefreshKilo:     envInt("UPSTREAM_REFRESH_KILO", 60),
		UpstreamRefreshLLM7:     envInt("UPSTREAM_REFRESH_LLM7", 300),
	}

	// Single-binary mode: in-process SOCKS on 127.0.0.1:9050 when VPN enabled,
	// direct otherwise. Keep VPNGateHost compat: if user explicitly set
	// VPNGATE_HOST != 127.0.0.1 (docker), honor it for backwards compat.
	if !cfg.VPNEnabled || cfg.VPNProvider == "direct" {
		cfg.SOCKSAddr = ""
	} else if os.Getenv("VPNGATE_HOST") != "" {
		cfg.SOCKSAddr = cfg.VPNGateHost + ":" + strconv.Itoa(cfg.VPNGateSocksPort)
	} else {
		cfg.SOCKSAddr = "127.0.0.1:" + strconv.Itoa(cfg.VPNGateSocksPort)
	}
	return cfg
}

func (c *Config) Validate() error {
	var errs []string

	if c.UpstreamURLOpenCode == "" {
		errs = append(errs, "UPSTREAM_URL_OPENCODE is required")
	}
	if c.UpstreamURLKilo == "" {
		errs = append(errs, "UPSTREAM_URL_KILO is required")
	}
	if c.UpstreamURLLLM7 == "" {
		errs = append(errs, "UPSTREAM_URL_LLM7 is required")
	}
	if c.VPNEnabled && c.SOCKSAddr == "" {
		errs = append(errs, "SOCKSAddr must be set when VPN_ENABLED is true")
	}
	if c.VPNProvider != "auto" && c.VPNProvider != "vpngate" && c.VPNProvider != "direct" {
		errs = append(errs, fmt.Sprintf("VPN_PROVIDER must be auto, vpngate or direct, got %q", c.VPNProvider))
	}
	if c.Port <= 0 || c.Port > 65535 {
		errs = append(errs, fmt.Sprintf("PORT must be between 1 and 65535, got %d", c.Port))
	}
	if c.VPNGateSocksPort <= 0 || c.VPNGateSocksPort > 65535 {
		errs = append(errs, fmt.Sprintf("VPNGATE_SOCKS_PORT must be between 1 and 65535, got %d", c.VPNGateSocksPort))
	}
	if c.VPNGateCtrlPort <= 0 || c.VPNGateCtrlPort > 65535 {
		errs = append(errs, fmt.Sprintf("VPNGATE_CTRL_PORT must be between 1 and 65535, got %d", c.VPNGateCtrlPort))
	}
	if c.VPNGateRotateInterval <= 0 {
		errs = append(errs, fmt.Sprintf("VPNGATE_ROTATE_INTERVAL must be positive, got %d", c.VPNGateRotateInterval))
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
