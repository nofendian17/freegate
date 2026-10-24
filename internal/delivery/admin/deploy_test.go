package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestVercelRelayCodeUsesBunRuntime guards the runtime switch that makes the
// relay's outbound TLS match the OpenCode CLI. The Edge runtime's fetch is
// rejected by OpenCode's free-tier gate, so the deployed handler must not
// force Edge and must use the Bun-compatible Web fetch export.
func TestVercelRelayCodeUsesBunRuntime(t *testing.T) {
	if strings.Contains(vercelRelayCode, `runtime: "edge"`) {
		t.Fatal("relay must not force the Edge runtime")
	}
	for _, want := range []string{"export default {", "async fetch(req)", `duplex: "half"`} {
		if !strings.Contains(vercelRelayCode, want) {
			t.Fatalf("relay code missing %q", want)
		}
	}
}

func TestVercelDeployBodyUsesBunRuntime(t *testing.T) {
	var body struct {
		Name   string `json:"name"`
		Target string `json:"target"`
		Files  []struct {
			File string `json:"file"`
			Data string `json:"data"`
		} `json:"files"`
	}
	if err := json.Unmarshal(vercelDeployBody("relay-test"), &body); err != nil {
		t.Fatalf("decode deploy body: %v", err)
	}
	if body.Name != "relay-test" || body.Target != "production" {
		t.Fatalf("unexpected deployment metadata: %+v", body)
	}
	files := make(map[string]string, len(body.Files))
	for _, f := range body.Files {
		files[f.File] = f.Data
	}
	if got := files["api/relay.js"]; got != vercelRelayCode {
		t.Fatal("deploy body does not use vercelRelayCode")
	}

	var pkg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(files["package.json"]), &pkg); err != nil {
		t.Fatalf("decode package.json: %v", err)
	}
	if pkg.Type != "module" {
		t.Fatalf("package.json type=%q, want module for ESM relay", pkg.Type)
	}

	var cfg struct {
		BunVersion string `json:"bunVersion"`
		Rewrites   []struct {
			Source      string `json:"source"`
			Destination string `json:"destination"`
		} `json:"rewrites"`
	}
	if err := json.Unmarshal([]byte(files["vercel.json"]), &cfg); err != nil {
		t.Fatalf("decode vercel.json: %v", err)
	}
	if cfg.BunVersion != "1.4.x" {
		t.Fatalf("bunVersion=%q, want 1.4.x", cfg.BunVersion)
	}
	if len(cfg.Rewrites) != 1 || cfg.Rewrites[0].Destination != "/api/relay" {
		t.Fatalf("unexpected rewrites: %+v", cfg.Rewrites)
	}
}
