package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"freegate/internal/model"
)

// mockUpstream implements handler.Upstream for testing.
type mockUpstream struct {
	chatCalled bool
	models     []model.Model
	ready      bool
	metrics    map[string]any
	lastParams anyllm.CompletionParams
}

func newMockUpstream() *mockUpstream {
	return &mockUpstream{
		metrics: map[string]any{"total_requests": int64(0)},
	}
}

func (m *mockUpstream) ProxyChat(w http.ResponseWriter, r *http.Request, params anyllm.CompletionParams) {
	m.chatCalled = true
	m.lastParams = params
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"choices":[]}`))
}

func (m *mockUpstream) AllModels() []model.Model { return m.models }
func (m *mockUpstream) IsReady() bool            { return m.ready }
func (m *mockUpstream) Metrics() map[string]any  { return m.metrics }

func TestHandler_Ready_Ready(t *testing.T) {
	u := newMockUpstream()
	u.ready = true
	h := New(u)
	req := httptest.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	var result map[string]string
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["status"] != "ok" {
		t.Errorf("expected status 'ok', got %q", result["status"])
	}
}

func TestHandler_Ready_NotReady(t *testing.T) {
	u := newMockUpstream()
	u.ready = false
	h := New(u)
	req := httptest.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", w.Code)
	}
}

func TestHandler_ListModels_Empty(t *testing.T) {
	u := newMockUpstream()
	u.models = nil
	h := New(u)
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", w.Code)
	}
}

func TestHandler_ListModels_WithData(t *testing.T) {
	u := newMockUpstream()
	u.models = []model.Model{
		{ID: "model-a", Object: "model", OwnedBy: "opencode", IsFree: true},
		{ID: "model-b", Object: "model", OwnedBy: "kilo", IsFree: true},
	}
	h := New(u)
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	var result model.ModelList
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result.Data) != 2 {
		t.Fatalf("expected 2 models, got %d", len(result.Data))
	}
}

func TestHandler_Metrics(t *testing.T) {
	u := newMockUpstream()
	u.metrics = map[string]any{"total_requests": int64(42)}
	h := New(u)
	req := httptest.NewRequest("GET", "/v1/metrics", nil)
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if result["total_requests"].(float64) != 42 {
		t.Errorf("expected total_requests=42, got %v", result["total_requests"])
	}
}

func TestHandler_Chat_Success(t *testing.T) {
	u := newMockUpstream()
	h := New(u)
	body := `{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if !u.chatCalled {
		t.Error("expected ProxyChat to be called")
	}
	if u.lastParams.Model != "deepseek-v4-flash-free" {
		t.Errorf("expected model 'deepseek-v4-flash-free', got %q", u.lastParams.Model)
	}
}

func TestHandler_Chat_DecodesCompletionParams(t *testing.T) {
	u := newMockUpstream()
	h := New(u)
	body := `{"model":"m1","messages":[{"role":"user","content":"hi"}],"temperature":0.7,"max_tokens":50,"tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{}}}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if u.lastParams.Temperature == nil || *u.lastParams.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", u.lastParams.Temperature)
	}
	if u.lastParams.MaxTokens == nil || *u.lastParams.MaxTokens != 50 {
		t.Errorf("MaxTokens = %v, want 50", u.lastParams.MaxTokens)
	}
	if len(u.lastParams.Tools) != 1 || u.lastParams.Tools[0].Function.Name != "f" {
		t.Errorf("Tools = %+v, want one function tool named f", u.lastParams.Tools)
	}
}

func TestHandler_Chat_RejectsEmptyMessages(t *testing.T) {
	u := newMockUpstream()
	h := New(u)
	body := `{"model":"m1","messages":[]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandler_Chat_EmptyBody(t *testing.T) {
	u := newMockUpstream()
	h := New(u)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(""))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestHandler_Chat_MissingModel(t *testing.T) {
	u := newMockUpstream()
	h := New(u)
	body := `{"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestHandler_Chat_InvalidJSON(t *testing.T) {
	u := newMockUpstream()
	h := New(u)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString("not json"))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestSyncRequestReasoningContent_FromReasoningContent(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello","reasoning_content":"deep thought"}]}`
	var params anyllm.CompletionParams
	if err := json.Unmarshal([]byte(body), &params); err != nil {
		t.Fatal(err)
	}
	// Without sync, Reasoning should be nil because JSON tag is "reasoning"
	if params.Messages[1].Reasoning != nil {
		t.Error("expected Reasoning to be nil before sync")
	}

	syncRequestReasoningContent(params, []byte(body))

	if params.Messages[1].Reasoning == nil {
		t.Fatal("expected Reasoning to be set after sync")
	}
	if params.Messages[1].Reasoning.Content != "deep thought" {
		t.Errorf("expected Reasoning.Content='deep thought', got %q", params.Messages[1].Reasoning.Content)
	}
}

func TestSyncRequestReasoningContent_UserMessage(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hi","reasoning_content":"should be ignored"}]}`
	var params anyllm.CompletionParams
	if err := json.Unmarshal([]byte(body), &params); err != nil {
		t.Fatal(err)
	}

	syncRequestReasoningContent(params, []byte(body))

	if params.Messages[0].Reasoning != nil {
		t.Error("expected Reasoning to be nil for user message")
	}
}

func TestSyncRequestReasoningContent_NoReasoningContent(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"assistant","content":"hello"}]}`
	var params anyllm.CompletionParams
	if err := json.Unmarshal([]byte(body), &params); err != nil {
		t.Fatal(err)
	}

	syncRequestReasoningContent(params, []byte(body))

	if params.Messages[0].Reasoning != nil {
		t.Error("expected Reasoning to remain nil")
	}
}

func TestSyncRequestReasoningContent_NonStringReasoningContent(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"assistant","content":"hello","reasoning_content":123}]}`
	var params anyllm.CompletionParams
	if err := json.Unmarshal([]byte(body), &params); err != nil {
		t.Fatal(err)
	}

	syncRequestReasoningContent(params, []byte(body))

	if params.Messages[0].Reasoning != nil {
		t.Error("expected Reasoning to remain nil for non-string reasoning_content")
	}
}

func TestSyncRequestReasoningContent_InvalidBody(t *testing.T) {
	body := `not json`
	var params anyllm.CompletionParams
	syncRequestReasoningContent(params, []byte(body))
	// Should not panic
}

func TestHandler_Chat_WithReasoningContent(t *testing.T) {
	u := newMockUpstream()
	h := New(u)
	body := `{"model":"deepseek-r1","messages":[{"role":"user","content":"think"},{"role":"assistant","content":"answer","reasoning_content":"step by step"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if u.lastParams.Messages[1].Reasoning == nil {
		t.Fatal("expected Reasoning to be set from reasoning_content")
	}
	if u.lastParams.Messages[1].Reasoning.Content != "step by step" {
		t.Errorf("expected Reasoning.Content='step by step', got %q", u.lastParams.Messages[1].Reasoning.Content)
	}
}
