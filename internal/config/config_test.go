package config

import (
	"os"
	"testing"
)

func TestValidate_Valid(t *testing.T) {
	cfg := defaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidate_EmptyOpenCodeURL(t *testing.T) {
	cfg := defaultConfig()
	cfg.UpstreamURLOpenCode = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty UPSTREAM_URL_OPENCODE")
	}
}

func TestValidate_EmptyKiloURL(t *testing.T) {
	cfg := defaultConfig()
	cfg.UpstreamURLKilo = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty UPSTREAM_URL_KILO")
	}
}

func TestValidate_InvalidPort(t *testing.T) {
	cfg := defaultConfig()
	cfg.Port = 99999
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid PORT")
	}
}

func TestValidate_NegativePort(t *testing.T) {
	cfg := defaultConfig()
	cfg.Port = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for PORT = 0")
	}
}

func TestValidate_EmptyPrefixes(t *testing.T) {
	cfg := defaultConfig()
	cfg.UpstreamKiloPrefixes = nil
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty prefixes")
	}
}

func TestEnvInt_Default(t *testing.T) {
	val := envInt("NONEXISTENT_KEY", 42)
	if val != 42 {
		t.Fatalf("expected 42, got %d", val)
	}
}

func TestEnvInt_Custom(t *testing.T) {
	os.Setenv("TEST_ENV_INT", "99")
	defer os.Unsetenv("TEST_ENV_INT")

	val := envInt("TEST_ENV_INT", 42)
	if val != 99 {
		t.Fatalf("expected 99, got %d", val)
	}
}

func TestEnvInt_Invalid(t *testing.T) {
	os.Setenv("TEST_ENV_INT2", "not-a-number")
	defer os.Unsetenv("TEST_ENV_INT2")

	val := envInt("TEST_ENV_INT2", 42)
	if val != 42 {
		t.Fatalf("expected default 42, got %d", val)
	}
}

func TestEnvSlice_Default(t *testing.T) {
	val := envSlice("NONEXISTENT_SLICE", "a,b,c")
	if len(val) != 3 || val[0] != "a" || val[1] != "b" || val[2] != "c" {
		t.Fatalf("expected [a b c], got %v", val)
	}
}

func TestEnvSlice_Custom(t *testing.T) {
	os.Setenv("TEST_ENV_SLICE", "x,y")
	defer os.Unsetenv("TEST_ENV_SLICE")

	val := envSlice("TEST_ENV_SLICE", "a,b,c")
	if len(val) != 2 || val[0] != "x" || val[1] != "y" {
		t.Fatalf("expected [x y], got %v", val)
	}
}

func TestEnvSlice_EmptyItem(t *testing.T) {
	os.Setenv("TEST_ENV_SLICE2", "a,,c")
	defer os.Unsetenv("TEST_ENV_SLICE2")

	val := envSlice("TEST_ENV_SLICE2", "")
	if len(val) != 2 || val[0] != "a" || val[1] != "c" {
		t.Fatalf("expected [a c], got %v", val)
	}
}

func defaultConfig() *Config {
	return &Config{
		Port:      1234,
		TorHost:   "127.0.0.1",
		TorPort:   9050,
		CtrlPort:  9051,
		LogLevel:  "info",
		RateLimit: 60,

		UpstreamURLOpenCode: "https://opencode.ai/zen/v1",
		UpstreamKeyOpenCode: "public",

		UpstreamURLKilo: "https://api.kilo.ai/api/openrouter",
		UpstreamKeyKilo: "anonymous",

		UpstreamDefault:      "opencode",
		UpstreamKiloPrefixes: []string{"kilo/", "kilo-", "openrouter/"},
	}
}
