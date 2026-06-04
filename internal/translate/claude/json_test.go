package claude

import (
	"encoding/json"
	"testing"
)

func TestJSONToClaude(t *testing.T) {
	body := `{
		"id":"chatcmpl-123",
		"model":"gpt-4",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Hello world"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":3}
	}`

	result, err := JSONToClaude([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var claude map[string]any
	json.Unmarshal(result, &claude)

	if claude["type"] != "message" {
		t.Errorf("expected type=message, got %v", claude["type"])
	}
	if claude["role"] != "assistant" {
		t.Errorf("expected role=assistant, got %v", claude["role"])
	}

	content, ok := claude["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("expected content array, got %v", claude["content"])
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Errorf("expected block type=text, got %v", block["type"])
	}
	if block["text"] != "Hello world" {
		t.Errorf("expected text=Hello world, got %v", block["text"])
	}

	usage, ok := claude["usage"].(map[string]any)
	if !ok {
		t.Fatal("expected usage")
	}
	if usage["input_tokens"] != float64(10) {
		t.Errorf("expected input_tokens=10, got %v", usage["input_tokens"])
	}
	if usage["output_tokens"] != float64(3) {
		t.Errorf("expected output_tokens=3, got %v", usage["output_tokens"])
	}

	stopReason, _ := claude["stop_reason"].(string)
	if stopReason != "end_turn" {
		t.Errorf("expected stop_reason=end_turn, got %v", stopReason)
	}
}

func TestJSONToClaudeWithTools(t *testing.T) {
	body := `{
		"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":5,"completion_tokens":10}
	}`

	result, err := JSONToClaude([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var claude map[string]any
	json.Unmarshal(result, &claude)

	content := claude["content"].([]any)
	block := content[0].(map[string]any)
	if block["type"] != "tool_use" {
		t.Errorf("expected tool_use block, got %v", block["type"])
	}
	if block["name"] != "get_weather" {
		t.Errorf("expected name=get_weather, got %v", block["name"])
	}

	stopReason := claude["stop_reason"].(string)
	if stopReason != "tool_use" {
		t.Errorf("expected stop_reason=tool_use, got %v", stopReason)
	}
}

func TestExtractUsage(t *testing.T) {
	u := extractUsage(map[string]any{
		"prompt_tokens":     20.0,
		"completion_tokens": 10.0,
	})
	if u.InputTokens != 20 || u.OutputTokens != 10 {
		t.Errorf("expected input=20 output=10, got %+v", u)
	}
}

func TestExtractUsageWithCache(t *testing.T) {
	u := extractUsage(map[string]any{
		"prompt_tokens":     25.0,
		"completion_tokens": 10.0,
		"prompt_tokens_details": map[string]any{
			"cached_tokens":         5.0,
			"cache_creation_tokens": 3.0,
		},
	})
	if u.InputTokens != 20 {
		t.Errorf("expected input_tokens=20 (25-5 cached), got %d", u.InputTokens)
	}
	if u.CacheReadTokens != 5 {
		t.Errorf("expected cache_read=5, got %d", u.CacheReadTokens)
	}
	if u.CacheCreateTokens != 3 {
		t.Errorf("expected cache_create=3, got %d", u.CacheCreateTokens)
	}
}
