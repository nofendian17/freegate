package config

import "testing"

// TestLoad_RawUpstreamLogDisabledByDefault guards the privacy contract:
// raw upstream response logging must stay OFF unless explicitly enabled via
// UPSTREAM_CAPTURE=true — logged lines contain full conversation content.
func TestLoad_RawUpstreamLogDisabledByDefault(t *testing.T) {
	t.Setenv("UPSTREAM_CAPTURE", "")

	cfg := Load()
	if cfg.UpstreamCapture {
		t.Error("UpstreamCapture must default to false")
	}
}

func TestLoad_RawUpstreamLogEnabledViaEnv(t *testing.T) {
	t.Setenv("UPSTREAM_CAPTURE", "true")

	cfg := Load()
	if !cfg.UpstreamCapture {
		t.Error("UpstreamCapture should be true when env is set to true")
	}
}
