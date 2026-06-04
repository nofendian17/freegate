package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"freegate/internal/model"
)

// mockChat implements handler.ChatProxy for testing.
type mockChat struct {
	chatCalled  bool
	lastModelID string
	lastBody    []byte
}

func (m *mockChat) ProxyChat(ctx context.Context, w http.ResponseWriter, r *http.Request, modelID string, body []byte) error {
	m.chatCalled = true
	m.lastModelID = modelID
	m.lastBody = body
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"choices":[]}`))
	return nil
}

type mockModels struct {
	models []model.Model
	ready  bool
}

func (m *mockModels) AllModels() []model.Model { return m.models }
func (m *mockModels) IsReady() bool            { return m.ready }

type mockMetrics struct {
	data map[string]any
}

func (m *mockMetrics) Metrics() map[string]any { return m.data }

func newMockHandler() (*Handler, *mockChat, *mockModels, *mockMetrics) {
	chat := &mockChat{}
	models := &mockModels{}
	mtr := &mockMetrics{data: map[string]any{"total_requests": int64(0)}}
	return New(chat, models, mtr), chat, models, mtr
}

func TestHandler_Root(t *testing.T) {
	h, _, _, _ := newMockHandler()
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	h.Root(w, req)

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
	h, _, models, _ := newMockHandler()
	models.ready = true
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
	h, _, _, _ := newMockHandler()
	req := httptest.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", w.Code)
	}
}

func TestHandler_ListModels_Empty(t *testing.T) {
	h, _, _, _ := newMockHandler()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", w.Code)
	}
}

func TestHandler_ListModels_WithData(t *testing.T) {
	h, _, models, _ := newMockHandler()
	models.models = []model.Model{
		{ID: "model-a", Object: "model", OwnedBy: "opencode", IsFree: true},
		{ID: "model-b", Object: "model", OwnedBy: "kilo", IsFree: true},
	}
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
	h, _, _, mtr := newMockHandler()
	mtr.data = map[string]any{"total_requests": int64(42)}
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
	h, chat, _, _ := newMockHandler()
	body := `{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if !chat.chatCalled {
		t.Error("expected ProxyChat to be called")
	}
	if chat.lastModelID != "deepseek-v4-flash-free" {
		t.Errorf("expected model ID 'deepseek-v4-flash-free', got %q", chat.lastModelID)
	}
}

func TestHandler_Chat_EmptyBody(t *testing.T) {
	h, _, _, _ := newMockHandler()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(""))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestHandler_Chat_MissingModel(t *testing.T) {
	h, _, _, _ := newMockHandler()
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
	h, _, _, _ := newMockHandler()
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString("not json"))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}
