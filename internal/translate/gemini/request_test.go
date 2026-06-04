package gemini

import (
	"encoding/json"
	"testing"
)

func TestGeminiToOpenAI_Basic(t *testing.T) {
	gemini := `{"contents":[{"parts":[{"text":"hi"}],"role":"user"}]}`
	result, err := ToOpenAI([]byte(gemini))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	msgs := openai["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg := msgs[0].(map[string]any)
	if msg["role"] != "user" {
		t.Errorf("expected role=user, got %v", msg["role"])
	}
	if msg["content"] != "hi" {
		t.Errorf("expected content=hi, got %v", msg["content"])
	}
}

func TestGeminiToOpenAI_SystemInstruction(t *testing.T) {
	gemini := `{"systemInstruction":{"parts":[{"text":"You are helpful"}]},"contents":[{"parts":[{"text":"hi"}],"role":"user"}]}`
	result, err := ToOpenAI([]byte(gemini))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	msgs := openai["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" {
		t.Errorf("expected role=system, got %v", sys["role"])
	}
}

func TestGeminiToOpenAI_GenerationConfig(t *testing.T) {
	gemini := `{"contents":[{"parts":[{"text":"hi"}],"role":"user"}],"generationConfig":{"temperature":0.7,"maxOutputTokens":100,"topP":0.9,"stopSequences":["stop"]}}`
	result, err := ToOpenAI([]byte(gemini))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	if openai["temperature"] != float64(0.7) {
		t.Errorf("expected temperature=0.7, got %v", openai["temperature"])
	}
	if openai["max_tokens"] != float64(100) {
		t.Errorf("expected max_tokens=100, got %v", openai["max_tokens"])
	}
	if openai["top_p"] != float64(0.9) {
		t.Errorf("expected top_p=0.9, got %v", openai["top_p"])
	}
}

func TestGeminiToOpenAI_Image(t *testing.T) {
	gemini := `{"contents":[{"parts":[{"text":"what is this?"},{"inlineData":{"mimeType":"image/jpeg","data":"/9j/4AAQ"}}],"role":"user"}]}`
	result, err := ToOpenAI([]byte(gemini))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	msgs := openai["messages"].([]any)
	msg := msgs[0].(map[string]any)
	content, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("expected content array")
	}
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(content))
	}
	img := content[1].(map[string]any)
	if img["type"] != "image_url" {
		t.Errorf("expected image_url type, got %v", img["type"])
	}
}

func TestGeminiToOpenAI_ModelField(t *testing.T) {
	gemini := `{"model":"gemini-pro","contents":[{"parts":[{"text":"hi"}],"role":"user"}]}`
	result, err := ToOpenAI([]byte(gemini))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var openai map[string]any
	json.Unmarshal(result, &openai)
	if openai["model"] != "gemini-pro" {
		t.Errorf("expected model=gemini-pro, got %v", openai["model"])
	}
}

func TestGeminiToOpenAI_EmptyBody(t *testing.T) {
	_, err := ToOpenAI([]byte{})
	if err == nil {
		t.Error("expected error for empty body")
	}
}

func TestGeminiToOpenAI_RoleModel(t *testing.T) {
	gemini := `{"contents":[{"parts":[{"text":"hi"}],"role":"user"},{"parts":[{"text":"hello"}],"role":"model"}]}`
	result, err := ToOpenAI([]byte(gemini))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var openai map[string]any
	json.Unmarshal(result, &openai)
	msgs := openai["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages")
	}
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" {
		t.Errorf("expected role=assistant, got %v", asst["role"])
	}
}

func TestProcessGeminiChunk(t *testing.T) {
	state := NewGeminiStreamState()

	// First chunk
	chunk := map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0.0,
				"delta": map[string]any{"content": "Hello "},
			},
		},
	}
	events := processGeminiChunk(chunk, state)
	if len(events) == 0 {
		t.Fatal("expected events")
	}
	if state.textBuffer != "Hello " {
		t.Errorf("expected textBuffer=Hello , got %s", state.textBuffer)
	}

	// Second chunk
	chunk2 := map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0.0,
				"delta": map[string]any{"content": "world"},
			},
		},
	}
	processGeminiChunk(chunk2, state)
	if state.textBuffer != "Hello world" {
		t.Errorf("expected textBuffer=Hello world, got %s", state.textBuffer)
	}

	// Finish chunk
	chunk3 := map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0.0,
				"delta": map[string]any{},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens": 5.0, "completion_tokens": 3.0,
		},
	}
	events3 := processGeminiChunk(chunk3, state)
	if !state.closed {
		t.Error("expected state to be closed")
	}
	var lastChunk map[string]any
	for _, e := range events3 {
		// Find the data line
		if len(e) > 6 && e[:6] == "data: " {
			json.Unmarshal([]byte(e[6:]), &lastChunk)
		}
	}
	if lastChunk != nil {
		candidates := lastChunk["candidates"].([]any)
		cand := candidates[0].(map[string]any)
		if cand["finishReason"] != "STOP" {
			t.Errorf("expected finishReason=STOP, got %v", cand["finishReason"])
		}
		if um, ok := lastChunk["usageMetadata"]; ok {
			_ = um
		} else {
			t.Error("expected usageMetadata")
		}
	}
}

func TestProcessGeminiChunk_Empty(t *testing.T) {
	state := NewGeminiStreamState()
	events := processGeminiChunk(map[string]any{}, state)
	if events != nil {
		t.Error("expected nil for empty chunk")
	}
}
