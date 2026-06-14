package upstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"freegate/internal/model"
)

func TestMimoFree_Match(t *testing.T) {
	m := NewMimoFreeUpstream("", "")

	tests := []struct {
		modelID string
		want    bool
	}{
		{"mimo-auto", true},
		{"mimo-v2.5-pro", false},
		{"unknown-model", false},
		{"", false},
	}

	for _, tt := range tests {
		got := m.Match(tt.modelID)
		if got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.modelID, got, tt.want)
		}
	}
}

func TestMimoFree_ListModels(t *testing.T) {
	m := NewMimoFreeUpstream("", "")

	models, err := m.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ID != "mimo-auto" {
		t.Errorf("expected ID mimo-auto, got %s", models[0].ID)
	}
	if !models[0].IsFree {
		t.Error("expected IsFree=true")
	}
	if models[0].Provider != "mimo-free" {
		t.Errorf("expected Provider mimo-free, got %s", models[0].Provider)
	}
}

func TestMimoFree_Models(t *testing.T) {
	m := NewMimoFreeUpstream("", "")
	m.cache.Set([]model.Model{
		{ID: "mimo-auto", Object: "model", OwnedBy: "mimo-free", IsFree: true, Provider: "mimo-free"},
	})

	got := m.Models()
	if len(got) != 1 {
		t.Fatalf("expected 1 model, got %d", len(got))
	}
	if got[0].ID != "mimo-auto" {
		t.Errorf("expected mimo-auto, got %s", got[0].ID)
	}
}

func TestMimoFree_DeriveBootstrapURL(t *testing.T) {
	tests := []struct {
		chatURL string
		want    string
	}{
		{"https://api.xiaomimimo.com/api/free-ai/openai/chat", "https://api.xiaomimimo.com/api/free-ai/bootstrap"},
		{"https://api.xiaomimimo.com/api/free-ai/openai/chat/", "https://api.xiaomimimo.com/api/free-ai/bootstrap"},
		{"https://example.com/v1/chat", "https://example.com/v1/chat/bootstrap"},
		{"https://example.com/", "https://example.com/bootstrap"},
	}

	for _, tt := range tests {
		got := deriveMimoBootstrapURL(tt.chatURL)
		if got != tt.want {
			t.Errorf("deriveMimoBootstrapURL(%q) = %q, want %q", tt.chatURL, got, tt.want)
		}
	}
}

func TestMimoFree_InjectSystemMarker_AlreadyPresent(t *testing.T) {
	body := `{"model":"mimo-auto","messages":[{"role":"system","content":"You are MiMoCode, an interactive CLI tool that helps users with software engineering tasks."},{"role":"user","content":"hi"}]}`

	out, err := injectMimoSystemMarker([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != body {
		t.Errorf("expected unchanged body when marker already present:\ngot:  %s\nwant: %s", string(out), body)
	}
}

func TestMimoFree_InjectSystemMarker_NotPresent(t *testing.T) {
	body := `{"model":"mimo-auto","messages":[{"role":"user","content":"hi"}]}`

	out, err := injectMimoSystemMarker([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data map[string]any
	if err := json.Unmarshal(out, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	msgs := data["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after injection, got %d", len(msgs))
	}

	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" {
		t.Errorf("expected first message role=system, got %q", sys["role"])
	}
	content := sys["content"].(string)
	if !strings.Contains(content, mimoMarker) {
		t.Errorf("expected system message to contain marker")
	}

	usr := msgs[1].(map[string]any)
	if usr["role"] != "user" {
		t.Errorf("expected second message role=user, got %q", usr["role"])
	}
}

func TestMimoFree_InjectSystemMarker_NoMessages(t *testing.T) {
	body := `{"model":"mimo-auto"}`

	out, err := injectMimoSystemMarker([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != body {
		t.Errorf("expected unchanged body when no messages field")
	}
}

func TestMimoFree_InjectSystemMarker_InvalidJSON(t *testing.T) {
	body := `not json`

	out, err := injectMimoSystemMarker([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != body {
		t.Errorf("expected unchanged body on invalid JSON")
	}
}

func TestMimoFree_ParseJWTExp_Valid(t *testing.T) {
	exp := time.Now().Add(1 * time.Hour).Unix()
	claims := map[string]any{"exp": exp}
	payload, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	jwt := "header." + encoded + ".signature"

	got := parseMimoJWTExp(jwt)
	if got.Unix() != exp {
		t.Errorf("parseMimoJWTExp() = %v, want %v", got.Unix(), exp)
	}
}

func TestMimoFree_ParseJWTExp_Invalid(t *testing.T) {
	before := time.Now()

	got := parseMimoJWTExp("invalid-jwt")
	if got.Before(before) {
		t.Errorf("expected fallback expiry in the future, got %v", got)
	}
}

func TestMimoFree_ParseJWTExp_NoExpClaim(t *testing.T) {
	claims := map[string]any{"iss": "test"}
	payload, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	jwt := "header." + encoded + ".signature"

	got := parseMimoJWTExp(jwt)
	delta := time.Until(got)
	if delta < 30*time.Minute {
		t.Errorf("expected fallback expiry ~50m in future, got %v from now", delta)
	}
}

func TestMimoFree_GenerateFingerprint_Stable(t *testing.T) {
	a := generateMimoFingerprint()
	b := generateMimoFingerprint()
	if a != b {
		t.Errorf("expected stable fingerprint across calls: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Errorf("expected sha256 hex (64 chars), got %d", len(a))
	}
}

func TestMimoFree_GenerateSessionID(t *testing.T) {
	id := generateMimoSessionID()
	if !strings.HasPrefix(id, mimoSessionPrefix) {
		t.Errorf("expected prefix %s, got %s", mimoSessionPrefix, id)
	}
	if len(id) != len(mimoSessionPrefix)+mimoSessionLen {
		t.Errorf("expected length %d, got %d", len(mimoSessionPrefix)+mimoSessionLen, len(id))
	}

	seen := make(map[string]bool)
	for range 100 {
		id := generateMimoSessionID()
		if seen[id] {
			t.Errorf("duplicate session ID: %s", id)
		}
		seen[id] = true
	}
}


