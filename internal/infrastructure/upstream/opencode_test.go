package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestOpenCode(t *testing.T, body string) *OpenCodeUpstream {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	u := NewOpenCodeUpstream(srv.URL, "public", "", []string{"big-pickle"})
	u.client = NewHTTPClient(srv.URL, "public", "", map[string]string{"x-opencode-client": "desktop"})
	return u
}

func TestOpenCode_ListModels_FreeBySuffix(t *testing.T) {
	body := `{"object":"list","data":[
		{"id":"claude-opus-4-7","object":"model","created":1,"owned_by":"opencode"},
		{"id":"gpt-5-nano","object":"model","created":1,"owned_by":"opencode"},
		{"id":"deepseek-v4-flash-free","object":"model","created":1,"owned_by":"opencode"},
		{"id":"mimo-v2.5-free","object":"model","created":1,"owned_by":"opencode"},
		{"id":"qwen3.6-plus-free","object":"model","created":1,"owned_by":"opencode"},
		{"id":"big-pickle","object":"model","created":1,"owned_by":"opencode"}
	]}`
	o := newTestOpenCode(t, body)

	models, err := o.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := make(map[string]bool, len(models))
	for _, m := range models {
		if !m.IsFree {
			t.Errorf("expected IsFree=true for %s", m.ID)
		}
		if m.Provider != "opencode" {
			t.Errorf("expected Provider=opencode for %s, got %q", m.ID, m.Provider)
		}
		got[m.ID] = true
	}

	for _, want := range []string{"deepseek-v4-flash-free", "mimo-v2.5-free", "qwen3.6-plus-free", "big-pickle"} {
		if !got[want] {
			t.Errorf("expected %s in free list", want)
		}
	}
	for _, paid := range []string{"claude-opus-4-7", "gpt-5-nano"} {
		if got[paid] {
			t.Errorf("did not expect paid/non-suffixed model %s in free list", paid)
		}
	}
}

func TestOpenCode_ListModels_Dedup(t *testing.T) {
	body := `{"object":"list","data":[
		{"id":"x-free","object":"model","created":1,"owned_by":"opencode"},
		{"id":"x-free","object":"model","created":1,"owned_by":"opencode"},
		{"id":"y-free","object":"model","created":1,"owned_by":"opencode"}
	]}`
	o := newTestOpenCode(t, body)

	models, err := o.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Errorf("expected 2 unique models after dedup, got %d", len(models))
	}
}

func TestOpenCode_ListModels_Empty(t *testing.T) {
	o := newTestOpenCode(t, `{"object":"list","data":[]}`)

	models, err := o.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected empty list, got %d", len(models))
	}
}

func TestOpenCode_ListModels_CustomAllowlist(t *testing.T) {
	body := `{"object":"list","data":[
		{"id":"big-pickle","object":"model","created":1,"owned_by":"opencode"},
		{"id":"my-special-model","object":"model","created":1,"owned_by":"opencode"},
		{"id":"claude-opus-4-7","object":"model","created":1,"owned_by":"opencode"}
	]}`
	o := newTestOpenCodeWithAllowlist(t, body, []string{"my-special-model"})

	models, err := o.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := make(map[string]bool, len(models))
	for _, m := range models {
		got[m.ID] = true
	}
	if !got["my-special-model"] {
		t.Error("expected my-special-model in free list (via allowlist)")
	}
	if got["claude-opus-4-7"] {
		t.Error("did not expect claude-opus-4-7 in free list")
	}
}

func newTestOpenCodeWithAllowlist(t *testing.T, body string, allowlist []string) *OpenCodeUpstream {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	u := NewOpenCodeUpstream(srv.URL, "public", "", allowlist)
	u.client = NewHTTPClient(srv.URL, "public", "", map[string]string{"x-opencode-client": "desktop"})
	return u
}
