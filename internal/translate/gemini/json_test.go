package gemini

import (
	"encoding/json"
	"testing"
)

func TestJSONToGemini(t *testing.T) {
	body := `{
		"choices":[{"index":0,"message":{"role":"assistant","content":"Hello world"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":3}
	}`

	result, err := JSONToGemini([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var gemini map[string]any
	json.Unmarshal(result, &gemini)

	candidates, ok := gemini["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		t.Fatalf("expected candidates array")
	}
	cand := candidates[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("expected finishReason=STOP, got %v", cand["finishReason"])
	}

	content := cand["content"].(map[string]any)
	parts := content["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	part := parts[0].(map[string]any)
	if part["text"] != "Hello world" {
		t.Errorf("expected text=Hello world, got %v", part["text"])
	}

	um, ok := gemini["usageMetadata"].(map[string]any)
	if !ok {
		t.Fatal("expected usageMetadata")
	}
	if um["promptTokenCount"] != float64(10) {
		t.Errorf("expected promptTokenCount=10, got %v", um["promptTokenCount"])
	}
}

func TestMapFinishReasonGemini(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"stop", "STOP"},
		{"length", "MAX_TOKENS"},
		{"content_filter", "BLOCKED"},
		{"tool_calls", "STOP"},
	}
	for _, tt := range tests {
		got := MapFinishReasonGemini(tt.input)
		if got != tt.want {
			t.Errorf("MapFinishReasonGemini(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
