package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"freegate/internal/domain"
)

func TestKilo_ListModels_ContextWindow(t *testing.T) {
	// Kilo decodes context_length today but drops it when building the
	// domain.Model; clients of /v1/models need it.
	body := `{"data":[
		{"id":"kilo-auto/free","isFree":true,"context_length":256000,"top_provider":{"context_length":256000,"max_completion_tokens":32768}},
		{"id":"nvidia/nemotron:free","isFree":true,"context_length":1048576},
		{"id":"paid/model","isFree":false,"context_length":1000}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	k := NewKiloUpstreamWithTransport(srv.URL, "", nil)

	models, err := k.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 free models, got %d", len(models))
	}
	got := make(map[string]int64, len(models))
	for _, m := range models {
		got[m.ID] = m.ContextWindow
	}
	if got["kilo-auto/free"] != 256000 {
		t.Errorf("kilo-auto/free context_window = %d, want 256000", got["kilo-auto/free"])
	}
	if got["nvidia/nemotron:free"] != 1048576 {
		t.Errorf("nemotron context_window = %d, want 1048576", got["nvidia/nemotron:free"])
	}
	// Kilo publishes context_length only; map it into the native limit object
	// alongside max_completion_tokens as output when top_provider supplies it.
	for _, m := range models {
		if m.ID == "kilo-auto/free" {
			if m.Limit == nil || m.Limit.Context != 256000 || m.Limit.Output != 32768 {
				t.Errorf("kilo limit = %+v, want context=256000 output=32768", m.Limit)
			}
		}
		if m.ID == "nvidia/nemotron:free" && (m.Limit == nil || m.Limit.Context != 1048576) {
			t.Errorf("nemotron limit = %+v, want context=1048576", m.Limit)
		}
	}
}

func TestKilo_Match_PrefixInCache(t *testing.T) {
	k := NewKiloUpstreamWithTransport("", "", nil)
	k.cache.Set([]domain.Model{
		{ID: "kilo-auto/free", Provider: "kilo"},
		{ID: "openrouter/owl-alpha", Provider: "kilo"},
		{ID: "openrouter/free", Provider: "kilo"},
	})

	tests := []struct {
		modelID string
		want    bool
	}{
		{"kilo-auto/free", true},
		{"openrouter/owl-alpha", true},
		{"openrouter/free", true},
		{"kilo-default", false},
		{"openrouter/paid", false},
		{"unknown-model", false},
		{"", false},
	}

	for _, tt := range tests {
		got := k.Match(tt.modelID)
		if got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.modelID, got, tt.want)
		}
	}
}

func TestKilo_Match_FreeSuffixInCache(t *testing.T) {
	k := NewKiloUpstreamWithTransport("", "", nil)
	k.cache.Set([]domain.Model{
		{ID: "nvidia/nemotron-3-super-120b-a12b:free", Provider: "kilo"},
		{ID: "poolside/laguna-m.1:free", Provider: "kilo"},
		{ID: "poolside/laguna-xs.2:free", Provider: "kilo"},
		{ID: "nvidia/nemotron-3-nano-omni:free", Provider: "kilo"},
		{ID: "qwen/qwen3.7-plus:free", Provider: "kilo"},
	})

	tests := []struct {
		modelID string
		want    bool
	}{
		{"nvidia/nemotron-3-super-120b-a12b:free", true},
		{"poolside/laguna-m.1:free", true},
		{"poolside/laguna-xs.2:free", true},
		{"nvidia/nemotron-3-nano-omni:free", true},
		{"qwen/qwen3.7-plus:free", true},
		{"model-without-suffix", false},
		{"model:notfree", false},
	}

	for _, tt := range tests {
		got := k.Match(tt.modelID)
		if got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.modelID, got, tt.want)
		}
	}
}

func TestKilo_Match_EmptyCache(t *testing.T) {
	k := NewKiloUpstreamWithTransport("", "", nil)

	if k.Match("kilo-auto/free") {
		t.Error("expected no match when cache is empty")
	}
	if k.Match("openrouter/owl-alpha") {
		t.Error("expected no match when cache is empty")
	}
	if k.Match("nvidia/nemotron-3-super-120b-a12b:free") {
		t.Error("expected no match when cache is empty")
	}
}
