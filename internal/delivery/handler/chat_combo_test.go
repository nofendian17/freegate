package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"freegate/internal/application"
	"freegate/internal/domain"
	"freegate/internal/infrastructure/upstream"
)

type zenWire struct {
	mu       sync.Mutex
	messages [][]byte
	chat     [][]byte
	headers  []http.Header
	failMsg  bool
	fail403  bool
}

func (z *zenWire) serve(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		z.mu.Lock()
		z.headers = append(z.headers, r.Header.Clone())
		z.mu.Unlock()
		switch r.URL.Path {
		case "/messages":
			z.mu.Lock()
			z.messages = append(z.messages, body)
			fail := z.failMsg
			fail403 := z.fail403
			z.mu.Unlock()
			if fail403 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"FreeTierError","message":"Error from provider (Console): OpenCode's free tier can only be used from within OpenCode"}}`)
				return
			}
			if fail {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":{"message":"busy"}}`)
				return
			}
			var raw struct {
				Model string `json:"model"`
				Tools []struct {
					Name        string         `json:"name"`
					InputSchema map[string]any `json:"input_schema"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(body, &raw); err != nil {
				t.Errorf("messages: invalid json: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if raw.Model != "union-alpha" {
				t.Errorf("messages: model=%q", raw.Model)
			}
			if len(raw.Tools) != 1 || raw.Tools[0].Name != "get_weather" || raw.Tools[0].InputSchema == nil {
				t.Errorf("messages: tools must carry name/input_schema, got %s", body)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if _, ok := raw.Tools[0].InputSchema["properties"]; !ok {
				t.Errorf("messages: input_schema lost properties, got %s", body)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var probe struct {
				Stream *bool `json:"stream"`
			}
			_ = json.Unmarshal(body, &probe)
			if probe.Stream != nil && *probe.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_start\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"union-alpha","content":[],"usage":{"input_tokens":8,"output_tokens":1}}}`+"\n\n")
				_, _ = io.WriteString(w, "event: content_block_start\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
				_, _ = io.WriteString(w, "event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`+"\n\n")
				_, _ = io.WriteString(w, "event: content_block_stop\n"+`data: {"type":"content_block_stop","index":0}`+"\n\n")
				_, _ = io.WriteString(w, "event: message_delta\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`+"\n\n")
				_, _ = io.WriteString(w, "event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"union-alpha","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":5}}`)
		case "/chat/completions":
			z.mu.Lock()
			z.chat = append(z.chat, body)
			z.mu.Unlock()
			var raw struct {
				Model string `json:"model"`
				Tools []struct {
					Type     string `json:"type"`
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(body, &raw); err != nil {
				t.Errorf("chat: invalid json: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if len(raw.Tools) != 1 || raw.Tools[0].Type != "function" || raw.Tools[0].Function.Name != "get_weather" {
				t.Errorf("chat: openai tools shape broken, got %s", body)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"`+raw.Model+`","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":5,"total_tokens":13}}`)
		default:
			http.NotFound(w, r)
		}
	}
}

func comboTestHandler(t *testing.T, z *zenWire, tiers []upstream.ComboTierInput) *Handler {
	t.Helper()
	srv := httptest.NewServer(z.serve(t))
	t.Cleanup(srv.Close)
	oc := upstream.NewOpenCodeUpstream(srv.URL, []string{"public"}, nil, nil)
	cr := upstream.NewComboRouter(upstream.NewRouter(oc))
	cr.RebuildCombos([]upstream.ComboTierRow{{Name: "assistant", Tiers: tiers}}, func(name string) domain.Upstream {
		return oc
	})
	return New(application.NewChatService(cr, nil), nil, nil)
}

func openAIToolsBody(model, path string, stream bool) string {
	var buf bytes.Buffer
	buf.WriteString(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]`)
	if stream {
		buf.WriteString(`,"stream":true`)
	}
	buf.WriteString(`}`)
	_ = path
	return buf.String()
}

func TestChat_ComboMessagesTier_OpenAIClient(t *testing.T) {
	z := &zenWire{}
	h := comboTestHandler(t, z, []upstream.ComboTierInput{{Provider: "opencode", Model: "union-alpha"}})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(openAIToolsBody("assistant", "", false)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Chat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("client response not openai json: %v (%s)", err, rec.Body.String())
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello" {
		t.Fatalf("unexpected client body: %s", rec.Body.String())
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if len(z.messages) != 1 {
		t.Fatalf("expected 1 messages call, got %d chat=%d", len(z.messages), len(z.chat))
	}
}

func TestChat_ComboMessagesTier_ClaudeClient(t *testing.T) {
	z := &zenWire{}
	h := comboTestHandler(t, z, []upstream.ComboTierInput{{Provider: "opencode", Model: "union-alpha"}})
	body := `{"model":"assistant","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Chat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("client response not claude json: %v (%s)", err, rec.Body.String())
	}
	if resp.Type != "message" || len(resp.Content) != 1 || resp.Content[0].Text != "hello" {
		t.Fatalf("unexpected client body: %s", rec.Body.String())
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if len(z.messages) != 1 {
		t.Fatalf("expected 1 messages call, got %d chat=%d", len(z.messages), len(z.chat))
	}
}

func TestChat_ComboMessagesTier_Streaming(t *testing.T) {
	z := &zenWire{}
	h := comboTestHandler(t, z, []upstream.ComboTierInput{{Provider: "opencode", Model: "union-alpha"}})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(openAIToolsBody("assistant", "", true)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Chat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, "data: ") || !strings.Contains(out, "[DONE]") || !strings.Contains(out, "hello") {
		t.Fatalf("expected openai sse with content, got %s", out)
	}
	if strings.Contains(out, "event: message_start") {
		t.Fatalf("raw claude events leaked to openai client: %s", out)
	}
}

func TestChat_ComboMessagesTier_FailoverToOpenAI(t *testing.T) {
	z := &zenWire{failMsg: true}
	h := comboTestHandler(t, z, []upstream.ComboTierInput{
		{Provider: "opencode", Model: "union-alpha"},
		{Provider: "opencode", Model: "gpt-plain"},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(openAIToolsBody("assistant", "", false)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Chat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chatcmpl-1") {
		t.Fatalf("expected openai completion, got %s", rec.Body.String())
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if len(z.messages) != 1 || len(z.chat) != 1 {
		t.Fatalf("expected failover messages=1 chat=1, got %d/%d", len(z.messages), len(z.chat))
	}
	var raw struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(z.chat[0], &raw); err != nil || raw.Model != "gpt-plain" {
		t.Fatalf("failover tier model not rewritten, got %s", z.chat[0])
	}
}

func TestChat_DownstreamIdentityIgnoredUpstream(t *testing.T) {
	// Downstream Zen identity headers must NOT reach the upstream:
	// freegate always mints fresh canonical identity instead.
	z := &zenWire{}
	h := comboTestHandler(t, z, []upstream.ComboTierInput{{Provider: "opencode", Model: "union-alpha"}})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(openAIToolsBody("assistant", "", false)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "opencode/1.18.31")
	req.Header.Set("x-opencode-session", "ses_0afae3e4c001AmMPIe8RFqNeTF")
	req.Header.Set("x-opencode-request", "usr_testuser000000000000000001")
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-project", "prj_test0000000000000000000001")
	rec := httptest.NewRecorder()
	h.Chat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if len(z.headers) == 0 {
		t.Fatal("no upstream calls captured")
	}
	got := z.headers[0]
	for k, foreign := range map[string]string{
		"X-Opencode-Session": "ses_0afae3e4c001AmMPIe8RFqNeTF",
		"X-Opencode-Request": "usr_testuser000000000000000001",
		"X-Opencode-Client":  "cli",
		"X-Opencode-Project": "prj_test0000000000000000000001",
	} {
		if v := got.Get(k); v == foreign {
			t.Errorf("upstream %s forwarded downstream value %q, want minted", k, v)
		}
		if v := got.Get(k); v == "" {
			t.Errorf("upstream %s empty, want minted", k)
		}
	}
	if v := got.Get("X-Opencode-Client"); v != "desktop" {
		t.Errorf("upstream X-Opencode-Client = %q, want desktop", v)
	}
	if v := got.Get("X-Opencode-Project"); v != "global" {
		t.Errorf("upstream X-Opencode-Project = %q, want global", v)
	}
	// Minted UA carries provider-utils/runtime suffixes, so it must never
	// equal the bare downstream value; equality means UA forwarding regressed.
	if v := got.Get("User-Agent"); v == "opencode/1.18.31" {
		t.Errorf("upstream User-Agent forwarded downstream value %q, want minted", v)
	}
}

func TestChat_ComboMessagesTier_FreeTierRejectionFailsOver(t *testing.T) {
	z := &zenWire{fail403: true}
	h := comboTestHandler(t, z, []upstream.ComboTierInput{
		{Provider: "opencode", Model: "union-alpha"},
		{Provider: "opencode", Model: "gpt-plain"},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(openAIToolsBody("assistant", "", false)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Chat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chatcmpl-1") {
		t.Fatalf("expected openai completion via failover tier, got %s", rec.Body.String())
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if len(z.messages) != 1 || len(z.chat) != 1 {
		t.Fatalf("expected failover messages=1 chat=1, got %d/%d", len(z.messages), len(z.chat))
	}
}
