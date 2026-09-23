package config

import (
	"bytes"
	"log"
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

func TestConfig_Load_AdminToken(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "0123456789abcdef0123456789abcdef")
	cfg := Load()
	if cfg.AdminToken != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("AdminToken failed: %s", cfg.AdminToken)
	}
}

// The API_KEY env var was removed in favour of DB-managed client keys; a
// stale value must produce a startup warning instead of failing silently.
func TestConfig_Load_APIKeyDeprecationWarning(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"unset", "", false},
		{"stale value", "legacy-key", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("API_KEY", tt.value)
			var buf bytes.Buffer
			log.SetOutput(&buf)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })

			Load()

			if got := strings.Contains(buf.String(), "API_KEY is no longer supported"); got != tt.want {
				t.Fatalf("warning = %v, want %v (output %q)", got, tt.want, buf.String())
			}
		})
	}
}

func TestConfig_Validate_AdminRequired(t *testing.T) {
	cfg := &Config{AdminToken: "", Port: 1234, RateLimit: 60, UpstreamURLOpenCode: "u", UpstreamURLKilo: "u", UpstreamURLLLM7: "u"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ADMIN_TOKEN") {
		t.Fatalf("expected ADMIN_TOKEN required error, got %v", err)
	}
}
func TestConfig_Validate_AdminTokenTooShort(t *testing.T) {
	cfg := &Config{AdminToken: "short", Port: 1234, RateLimit: 60, UpstreamURLOpenCode: "u", UpstreamURLKilo: "u", UpstreamURLLLM7: "u"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at least 6") {
		t.Fatalf("expected ADMIN_TOKEN length error, got %v", err)
	}
}

func TestLoad_ProvidersDBPath_Default(t *testing.T) {
	t.Setenv("PROVIDERS_DB_PATH", "")
	cfg := Load()
	if cfg.ProvidersDBPath != "./data/providers.db" {
		t.Fatalf("default providers db path, got %q", cfg.ProvidersDBPath)
	}
}

func TestLoad_ProvidersDBPath_Custom(t *testing.T) {
	t.Setenv("PROVIDERS_DB_PATH", "/tmp/x.db")
	cfg := Load()
	if cfg.ProvidersDBPath != "/tmp/x.db" {
		t.Fatalf("custom providers db path, got %q", cfg.ProvidersDBPath)
	}
}

func defaultConfig() *Config {
	return &Config{
		Port:       1234,
		AdminToken: "0123456789abcdef0123456789abcdef",
		LogLevel:   "info",
		RateLimit:  60,

		UpstreamURLOpenCode:           "https://opencode.ai/zen/v1",
		UpstreamKeyOpenCode:           []string{"public"},
		UpstreamOpenCodeFreeAllowlist: []string{"big-pickle"},

		UpstreamURLKilo: "https://api.kilo.ai/api/openrouter",
		UpstreamKeyKilo: "anonymous",

		UpstreamURLLLM7: "https://api.llm7.io/v1",

		UpstreamDefault: "opencode",
	}
}
