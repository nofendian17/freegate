package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"freegate/internal/model"
)

// mockUpstream implements handler.Upstream for testing.
type mockUpstream struct {
	chatCalled  bool
	models      []model.Model
	ready       bool
	metrics     map[string]any
	lastModelID string
	lastBody    []byte
}

func newMockUpstream() *mockUpstream {
	return &mockUpstream{
		metrics: map[string]any{"total_requests": int64(0)},
	}
}

func (m *mockUpstream) ProxyChat(w http.ResponseWriter, r *http.Request, modelID string, body []byte) {
	m.chatCalled = true
	m.lastModelID = modelID
	m.lastBody = body
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"choices":[]}`))
}

func (m *mockUpstream) AllModels() []model.Model { return m.models }
func (m *mockUpstream) IsReady() bool            { return m.ready }
func (m *mockUpstream) Metrics() map[string]any  { return m.metrics }

func TestHandler_Root(t *testing.T) {
	u := newMockUpstream()
	h := New(u)
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if result["service"] != "freegate - multi-upstream AI proxy" {
		t.Errorf("unexpected service field: %v", result["service"])
	}
}

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
	if u.lastModelID != "deepseek-v4-flash-free" {
		t.Errorf("expected model ID 'deepseek-v4-flash-free', got %q", u.lastModelID)
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
	var errResp map[string]map[string]string
	json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp["error"]["message"] != "missing required field: model" {
		t.Errorf("unexpected error: %v", errResp["error"]["message"])
	}
}

func TestHandler_Chat_InvalidJSON(t *testing.T) {
	u := newMockUpstream()
	h := New(u)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}
