package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeChatService struct {
	capturedBody  []byte
	capturedModel string
}

func (f *fakeChatService) ProxyChat(_ context.Context, w http.ResponseWriter, r *http.Request, modelID string, body []byte) error {
	f.capturedBody = body
	f.capturedModel = modelID
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","created_at":1700000000,"status":"completed","output":[{"type":"message","id":"msg_0","role":"assistant","content":[{"type":"output_text","text":"hello from muse"}]}]}`))
	return nil
}

func TestChat_OpenAIToResponses_DirectConfig(t *testing.T) {
	fake := &fakeChatService{}
	h := New(fake, nil, nil)
	body := `{"model":"muse-spark-1.2-contributor-free","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Chat(rec, req)
	// Check that upstream received responses format (has input, no messages)
	var captured map[string]any
	if err := json.Unmarshal(fake.capturedBody, &captured); err != nil {
		t.Fatalf("invalid captured json: %v", err)
	}
	if _, ok := captured["input"]; !ok {
		t.Errorf("expected input field for muse model (openai->responses), got %s", string(fake.capturedBody))
	}
	if _, ok := captured["messages"]; ok {
		t.Errorf("expected no messages field after translation, got %s", string(fake.capturedBody))
	}
	// Response should be translated back to chat.completion
	respBody := rec.Body.String()
	if !contains(respBody, "chat.completion") {
		t.Errorf("expected chat.completion in response, got %s", respBody)
	}
}

func TestChat_ClaudeToResponses_DirectConfig(t *testing.T) {
	fake := &fakeChatService{}
	h := New(fake, nil, nil)
	body := `{"model":"muse-spark-1.2-contributor-free","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Chat(rec, req)
	var captured map[string]any
	_ = json.Unmarshal(fake.capturedBody, &captured)
	if _, ok := captured["input"]; !ok {
		t.Errorf("expected input field for claude->responses, got %s", string(fake.capturedBody))
	}
	respBody := rec.Body.String()
	if !contains(respBody, `"type":"message"`) {
		t.Errorf("expected claude message type, got %s", respBody)
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}
