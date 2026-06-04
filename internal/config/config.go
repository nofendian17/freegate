package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port      int
	TorHost   string
	TorPort   int
	CtrlPort  int
	CtrlPass  string
	LogLevel  string
	APIKey    string
	RateLimit int

	UpstreamURLOpenCode string
	UpstreamKeyOpenCode string

	UpstreamURLKilo string
	UpstreamKeyKilo string

	UpstreamDefault string

	UpstreamRefreshOpenCode int
	UpstreamRefreshKilo     int

	SOCKSAddr string
}

func Load() *Config {
	cfg := &Config{
		Port:      envInt("PORT", 1234),
		TorHost:   envStr("TOR_HOST", "127.0.0.1"),
		TorPort:   envInt("TOR_PORT", 9050),
		CtrlPort:  envInt("TOR_CTRL_PORT", 9051),
		CtrlPass:  envStr("TOR_PASS", ""),
		LogLevel:  envStr("LOG_LEVEL", "info"),
		APIKey:    envStr("API_KEY", ""),
		RateLimit: envInt("RATE_LIMIT", 60),

		UpstreamURLOpenCode: envStr("UPSTREAM_URL_OPENCODE", "https://opencode.ai/zen/v1"),
		UpstreamKeyOpenCode: envStr("UPSTREAM_KEY_OPENCODE", "public"),

		UpstreamURLKilo: envStr("UPSTREAM_URL_KILO", "https://api.kilo.ai/api/openrouter"),
		UpstreamKeyKilo: envStr("UPSTREAM_KEY_KILO", "anonymous"),

		UpstreamDefault: envStr("UPSTREAM_DEFAULT", "opencode"),

		UpstreamRefreshOpenCode: envInt("UPSTREAM_REFRESH_OPENCODE", 60),
		UpstreamRefreshKilo:     envInt("UPSTREAM_REFRESH_KILO", 60),
	}

	cfg.SOCKSAddr = cfg.TorHost + ":" + strconv.Itoa(cfg.TorPort)
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
	if c.Port <= 0 || c.Port > 65535 {
		errs = append(errs, fmt.Sprintf("PORT must be between 1 and 65535, got %d", c.Port))
	}
	if c.TorPort <= 0 || c.TorPort > 65535 {
		errs = append(errs, fmt.Sprintf("TOR_PORT must be between 1 and 65535, got %d", c.TorPort))
	}
	if c.CtrlPort <= 0 || c.CtrlPort > 65535 {
		errs = append(errs, fmt.Sprintf("TOR_CTRL_PORT must be between 1 and 65535, got %d", c.CtrlPort))
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
