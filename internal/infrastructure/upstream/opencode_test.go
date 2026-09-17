package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"freegate/internal/translate"
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

	u := NewOpenCodeUpstream(srv.URL, []string{"public"}, nil, []string{"big-pickle"})
	u.client = NewHTTPClient(srv.URL, []string{"public"}, nil, map[string]string{"x-opencode-client": "desktop"})
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

	u := NewOpenCodeUpstream(srv.URL, []string{"public"}, nil, allowlist)
	u.client = NewHTTPClient(srv.URL, []string{"public"}, nil, map[string]string{"x-opencode-client": "desktop"})
	return u
}

func TestOpenCode_BuildURL_Routing(t *testing.T) {
	o := NewOpenCodeUpstream("http://example.com", []string{"public"}, nil, nil)
	cases := []struct {
		name  string
		model string
		body  string
		want  string
	}{
		{"muse-spark to responses", "muse-spark", `{"model":"muse-spark"}`, "/responses"},
		{"muse_spark variant", "muse_spark-pro", `{"model":"muse_spark-pro"}`, "/responses"},
		{"muse case-insensitive", "Muse-Spark-X", `{"model":"Muse-Spark-X"}`, "/responses"},
		{"union-alpha to messages", "union-alpha", `{"model":"union-alpha"}`, "/messages"},
		{"union-alpha variant substring", "my-union-alpha", `{"model":"my-union-alpha"}`, "/messages"},
		{"plain model to chat", "gpt-5", `{"model":"gpt-5"}`, "/chat/completions"},
		{"model-less responses body", "", `{"input":"hi"}`, "/responses"},
		{"thinking suffix stripped", "muse-spark(high)", `{"model":"muse-spark(high)"}`, "/responses"},
	}
	for _, tc := range cases {
		if got := o.buildURL(tc.model, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: buildURL(%q) = %q, want %q", tc.name, tc.model, got, tc.want)
		}
	}
}

func TestOpenCode_BuildURL_HonorsCustomConfig(t *testing.T) {
	o := NewOpenCodeUpstream("http://example.com", []string{"public"}, nil, nil)
	o.SetResponseModels([]string{"my-resp"})
	o.SetMessageModels([]string{"my-claude-model"})
	if got := o.buildURL("my-resp-1", []byte(`{"model":"my-resp-1"}`)); got != "/responses" {
		t.Errorf("custom RESPONSE_MODELS: got %q, want /responses", got)
	}
	if got := o.buildURL("my-claude-model", []byte(`{"model":"my-claude-model"}`)); got != "/messages" {
		t.Errorf("custom MESSAGE_MODELS: got %q, want /messages", got)
	}
	// Old defaults no longer route once overridden.
	if got := o.buildURL("muse-spark", []byte(`{"model":"muse-spark"}`)); got != "/chat/completions" {
		t.Errorf("overridden defaults: muse-spark got %q, want /chat/completions", got)
	}
}

func TestOpenCode_EnsureMessagesMaxTokens(t *testing.T) {
	out := ensureMessagesMaxTokens([]byte(`{"model":"union-alpha","messages":[]}`))
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["max_tokens"] != float64(4096) {
		t.Errorf("expected max_tokens=4096 default, got %v", raw["max_tokens"])
	}
	keep := ensureMessagesMaxTokens([]byte(`{"model":"m","max_tokens":100}`))
	var raw2 map[string]any
	if err := json.Unmarshal(keep, &raw2); err != nil {
		t.Fatal(err)
	}
	if raw2["max_tokens"] != float64(100) {
		t.Errorf("expected existing max_tokens preserved, got %v", raw2["max_tokens"])
	}
}

func TestOpenCode_BuildHeaders_ForwardsDownstreamIdentity(t *testing.T) {
	ctx := translate.WithDownstreamIdentity(context.Background(), translate.DownstreamIdentity{
		UserAgent: "opencode/1.18.31",
		Session:   "ses_0afae3e4c001AmMPIe8RFqNeTF",
		RequestID: "usr_testuser000000000000000001",
		Client:    "cli",
		Project:   "prj_test0000000000000000000001",
	})
	h := buildOpencodeHeaders(ctx, "/chat/completions", []byte(`{"model":"x"}`))
	for k, want := range map[string]string{
		"User-Agent":         "opencode/1.18.31",
		"x-opencode-session": "ses_0afae3e4c001AmMPIe8RFqNeTF",
		"x-opencode-request": "usr_testuser000000000000000001",
		"x-opencode-client":  "cli",
		"x-opencode-project": "prj_test0000000000000000000001",
	} {
		if h[k] != want {
			t.Errorf("%s = %q, want %q", k, h[k], want)
		}
	}
}

func TestOpenCode_BuildHeaders_InvalidDownstreamIdentityFallsBack(t *testing.T) {
	ctx := translate.WithDownstreamIdentity(context.Background(), translate.DownstreamIdentity{
		UserAgent: "claude-code/1.0",
		Session:   "claude:abc-123",
	})
	h := buildOpencodeHeaders(ctx, "/chat/completions", []byte(`{"model":"x"}`))
	if !strings.HasPrefix(h["User-Agent"], "opencode/") {
		t.Errorf("foreign UA forwarded: %q", h["User-Agent"])
	}
	if got := h["x-opencode-session"]; got != "ses_de183bb4be51r6rQJojApiDzDR" {
		t.Errorf("foreign session not translated, got %q", got)
	}
	if h["x-opencode-client"] != "desktop" || h["x-opencode-project"] != "global" {
		t.Errorf("defaults broken: %+v", h)
	}
	if !strings.HasPrefix(h["x-opencode-request"], "msg_") {
		t.Errorf("request id not generated: %q", h["x-opencode-request"])
	}
}

func TestOpenCode_BuildHeaders_OmitsXApiKey(t *testing.T) {
	// The genuine client never sends x-api-key (verified against
	// anomalyco/opencode source); the public marker is a non-genuine
	// fingerprint the free-tier gate rejects.
	for _, endpoint := range []string{"/chat/completions", "/messages", "/responses"} {
		h := buildOpencodeHeaders(context.Background(), endpoint, []byte(`{"model":"x"}`))
		if v, ok := h["x-api-key"]; ok {
			t.Errorf("endpoint %s: x-api-key = %q, want absent", endpoint, v)
		}
	}
}

func TestOpenCode_BuildHeaders_Messages(t *testing.T) {
	h := buildOpencodeHeaders(context.Background(), "/messages", []byte(`{"model":"union-alpha"}`))
	if h["anthropic-version"] != "2023-06-01" {
		t.Errorf("expected anthropic-version for /messages, got %q", h["anthropic-version"])
	}
	h2 := buildOpencodeHeaders(context.Background(), "/chat/completions", []byte(`{"model":"x"}`))
	if _, ok := h2["anthropic-version"]; ok {
		t.Errorf("did not expect anthropic-version for chat endpoint")
	}
	hs := buildOpencodeHeaders(context.Background(), "/chat/completions", []byte(`{"stream":true}`))
	if hs["Accept"] != "text/event-stream" {
		t.Errorf("expected streaming Accept, got %q", hs["Accept"])
	}
}

func TestOpenCode_AnonymousNonStreamUpgradedToStream(t *testing.T) {
	var gotStream any
	var gotStreamOptions any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		gotStream = raw["stream"]
		gotStreamOptions = raw["stream_options"]
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, assembleChatSSE)
	}))
	defer srv.Close()
	u := NewOpenCodeUpstream(srv.URL, []string{"public"}, nil, nil)
	resp, err := u.ChatCompletion(context.Background(), []byte(`{"model":"gpt-plain","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	defer resp.Close()
	if gotStream != true {
		t.Fatalf("upstream did not receive stream:true, got %v", gotStream)
	}
	if _, ok := gotStreamOptions.(map[string]any); !ok {
		t.Fatalf("stream_options missing: %v", gotStreamOptions)
	}
	rawBody, _ := io.ReadAll(resp.Body)
	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rawBody, &out); err != nil {
		t.Fatalf("assembled body not json: %v\n%s", err, rawBody)
	}
	if out.Object != "chat.completion" || len(out.Choices) != 1 || out.Choices[0].Message.Content != "hello" {
		t.Fatalf("bad assembled body: %s", rawBody)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	if resp.Format != "openai" {
		t.Fatalf("format = %q", resp.Format)
	}
}

func TestOpenCode_MixedKeysNeverUpgrade(t *testing.T) {
	var gotStream []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		gotStream = append(gotStream, raw["stream"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	// Mixed public/real keys: the upgrade decision must be deterministic
	// and must not consume rotation slots, so it never upgrades.
	u := NewOpenCodeUpstream(srv.URL, []string{"public", "sk-real"}, nil, nil)
	for range 4 {
		resp, err := u.ChatCompletion(context.Background(), []byte(`{"model":"gpt-plain","messages":[]}`))
		if err != nil {
			t.Fatalf("chat: %v", err)
		}
		resp.Close()
	}
	for _, s := range gotStream {
		if s != nil {
			t.Fatalf("mixed keys upgraded to stream=%v", s)
		}
	}
}

func TestOpenCode_StreamOptionsOnlyOnChat(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	u := NewOpenCodeUpstream(srv.URL, []string{"public"}, nil, nil)
	resp, err := u.ChatCompletion(context.Background(), []byte(`{"model":"union-alpha","messages":[]}`))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	defer resp.Close()
	if got["stream"] != true {
		t.Fatalf("stream not enabled: %v", got["stream"])
	}
	if _, ok := got["stream_options"]; ok {
		t.Fatalf("stream_options leaked into Messages body: %v", got["stream_options"])
	}
}

func TestOpenCode_KeyedNonStreamPassesThrough(t *testing.T) {
	var gotStream any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		gotStream = raw["stream"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	u := NewOpenCodeUpstream(srv.URL, []string{"sk-real"}, nil, nil)
	resp, err := u.ChatCompletion(context.Background(), []byte(`{"model":"gpt-plain","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	defer resp.Close()
	if gotStream != nil {
		t.Fatalf("keyed request unexpectedly upgraded, stream=%v", gotStream)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestOpenCode_UpgradeNonSSEErrorPassesThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"slow"}`)
	}))
	defer srv.Close()
	u := NewOpenCodeUpstream(srv.URL, []string{"public"}, nil, nil)
	resp, err := u.ChatCompletion(context.Background(), []byte(`{"model":"gpt-plain","messages":[]}`))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	defer resp.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429 passthrough", resp.StatusCode)
	}
}

func TestOpenCode_GenID_Format(t *testing.T) {
	for _, p := range []string{"ses", "msg"} {
		id := genOpencodeID(p)
		if len(id) != len(p)+1+12+14 {
			t.Errorf("%s: unexpected length %d for %q", p, len(id), id)
		}
		if id[:len(p)+1] != p+"_" {
			t.Errorf("%s: bad prefix %q", p, id)
		}
	}
	if id := genSessionID(); !openCodeSessionRE.MatchString(id) {
		t.Errorf("genSessionID %q does not match canonical form", id)
	}
}

func TestOpenCode_AnonymousInjectsNoopTools_KeyedDoesNot(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = raw
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	// Streaming body so the request passes straight through (no SSE assembly).
	in := []byte(`{"model":"gpt-plain","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	anon := NewOpenCodeUpstream(srv.URL, []string{"public"}, nil, nil)
	resp, err := anon.ChatCompletion(context.Background(), in)
	if err != nil {
		t.Fatalf("anon chat: %v", err)
	}
	resp.Close()
	var raw map[string]any
	if err := json.Unmarshal(gotBody, &raw); err != nil {
		t.Fatalf("anon body not json: %v", err)
	}
	tools, ok := raw["tools"].([]any)
	if !ok {
		t.Fatalf("anon body missing injected stub tools: %s", gotBody)
	}
	expectGateNames(t, tools)

	keyed := NewOpenCodeUpstream(srv.URL, []string{"sk-real"}, nil, nil)
	resp, err = keyed.ChatCompletion(context.Background(), in)
	if err != nil {
		t.Fatalf("keyed chat: %v", err)
	}
	resp.Close()
	raw = nil
	if err := json.Unmarshal(gotBody, &raw); err != nil {
		t.Fatalf("keyed body not json: %v", err)
	}
	if _, ok := raw["tools"]; ok {
		t.Fatalf("keyed body must keep exact passthrough, got tools: %s", gotBody)
	}
}
