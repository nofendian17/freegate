package config

import (
	"os"
	"strings"
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

func TestValidate_EmptyLLM7URL(t *testing.T) {
	cfg := defaultConfig()
	cfg.UpstreamURLLLM7 = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty UPSTREAM_URL_LLM7")
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

func TestValidate_VPNEnabledRequiresSocksAddr(t *testing.T) {
	cfg := defaultConfig()
	cfg.VPNEnabled = true
	cfg.SOCKSAddr = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty SOCKSAddr when VPN_ENABLED is true")
	}
}

func TestValidate_VPNProviderInvalid(t *testing.T) {
	cfg := defaultConfig()
	cfg.VPNProvider = "invalid"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid VPN_PROVIDER")
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

func TestConfig_Load_MultiAPIKey(t *testing.T) {
	t.Setenv("API_KEY", "key1, key2, key3")
	t.Setenv("ADMIN_TOKEN", "0123456789abcdef0123456789abcdef")
	cfg := Load()
	if len(cfg.APIKey) != 3 || cfg.APIKey[0] != "key1" || cfg.APIKey[1] != "key2" || cfg.APIKey[2] != "key3" {
		t.Fatalf("APIKey split failed: %+v", cfg.APIKey)
	}
	if cfg.AdminToken != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("AdminToken failed: %s", cfg.AdminToken)
	}
}
func TestConfig_Validate_AdminRequired(t *testing.T) {
	cfg := &Config{AdminToken: "", APIKey: []string{"a"}, Port: 1234, VPNGateSocksPort: 9050, VPNGateCtrlPort: 8080, VPNGateRotateInterval: 30, RateLimit: 60, UpstreamURLOpenCode: "u", UpstreamURLKilo: "u", UpstreamURLLLM7: "u"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ADMIN_TOKEN") {
		t.Fatalf("expected ADMIN_TOKEN required error, got %v", err)
	}
}
func TestConfig_Validate_AdminTokenTooShort(t *testing.T) {
	cfg := &Config{AdminToken: "short", APIKey: []string{"a"}, Port: 1234, VPNGateSocksPort: 9050, VPNGateCtrlPort: 8080, VPNGateRotateInterval: 30, RateLimit: 60, UpstreamURLOpenCode: "u", UpstreamURLKilo: "u", UpstreamURLLLM7: "u"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at least 16") {
		t.Fatalf("expected ADMIN_TOKEN length error, got %v", err)
	}
}

func defaultConfig() *Config {
	return &Config{
		Port:                  1234,
		AdminToken:            "0123456789abcdef0123456789abcdef",
		APIKey:                []string{"test-key"},
		VPNEnabled:            true,
		VPNProvider:           "auto",
		VPNGateHost:           "127.0.0.1",
		VPNGateSocksPort:      9050,
		VPNGateCtrlPort:       8080,
		VPNGateRotateInterval: 30,
		LogLevel:              "info",
		RateLimit:             60,

		UpstreamURLOpenCode:           "https://opencode.ai/zen/v1",
		UpstreamKeyOpenCode:           []string{"public"},
		UpstreamOpenCodeFreeAllowlist: []string{"big-pickle"},

		UpstreamURLKilo: "https://api.kilo.ai/api/openrouter",
		UpstreamKeyKilo: "anonymous",

		UpstreamURLLLM7: "https://api.llm7.io/v1",

		UpstreamDefault: "opencode",
		SOCKSAddr:       "127.0.0.1:9050",
	}
}
