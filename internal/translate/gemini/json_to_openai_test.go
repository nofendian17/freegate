package gemini

import (
	"encoding/json"
	"testing"
)

func TestJSONToOpenAI_TextOnly(t *testing.T) {
	in := `{
		"candidates":[{
			"content":{"parts":[{"text":"hello"}],"role":"model"},
			"finishReason":"STOP"
		}],
		"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8}
	}`
	out, err := JSONToOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	choices, _ := got["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(choices))
	}
	c0, _ := choices[0].(map[string]any)
	if c0["finish_reason"] != "stop" {
		t.Errorf("finish_reason=%v want stop", c0["finish_reason"])
	}
	msg, _ := c0["message"].(map[string]any)
	if msg["content"] != "hello" {
		t.Errorf("content=%v want hello", msg["content"])
	}
	if _, has := msg["tool_calls"]; has {
		t.Errorf("expected no tool_calls for text-only response")
	}
	usage, _ := got["usage"].(map[string]any)
	if usage["prompt_tokens"].(float64) != 5 {
		t.Errorf("prompt_tokens=%v want 5", usage["prompt_tokens"])
	}
	if usage["completion_tokens"].(float64) != 3 {
		t.Errorf("completion_tokens=%v want 3", usage["completion_tokens"])
	}
	if usage["total_tokens"].(float64) != 8 {
		t.Errorf("total_tokens=%v want 8", usage["total_tokens"])
	}
}

func TestJSONToOpenAI_FunctionCall(t *testing.T) {
	in := `{
		"candidates":[{
			"content":{"parts":[
				{"text":"calling tool"},
				{"functionCall":{"name":"get_weather","args":{"city":"SF"}}}
			],"role":"model"},
			"finishReason":"STOP"
		}]
	}`
	out, err := JSONToOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	choices, _ := got["choices"].([]any)
	c0, _ := choices[0].(map[string]any)
	msg, _ := c0["message"].(map[string]any)
	tcs, _ := msg["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(tcs))
	}
	tc, _ := tcs[0].(map[string]any)
	fn, _ := tc["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("name=%v want get_weather", fn["name"])
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil {
		t.Fatalf("args not JSON: %v", err)
	}
	if args["city"] != "SF" {
		t.Errorf("args.city=%v want SF", args["city"])
	}
}

func TestJSONToOpenAI_FinishReason(t *testing.T) {
	tests := []struct {
		gemini, want string
	}{
		{"STOP", "stop"},
		{"MAX_TOKENS", "length"},
		{"SAFETY", "content_filter"},
		{"BLOCKED", "content_filter"},
		{"UNKNOWN", "stop"},
	}
	for _, tc := range tests {
		t.Run(tc.gemini, func(t *testing.T) {
			body := `{"candidates":[{"content":{"parts":[{"text":"x"}],"role":"model"},"finishReason":"` + tc.gemini + `"}]}`
			out, err := JSONToOpenAI([]byte(body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got map[string]any
			_ = json.Unmarshal(out, &got)
			choices, _ := got["choices"].([]any)
			c0, _ := choices[0].(map[string]any)
			if c0["finish_reason"] != tc.want {
				t.Errorf("finish_reason=%v want %v", c0["finish_reason"], tc.want)
			}
		})
	}
}

func TestJSONToOpenAI_UsageWithThinking(t *testing.T) {
	in := `{
		"candidates":[{"content":{"parts":[{"text":"x"}],"role":"model"},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"thoughtsTokenCount":6,"totalTokenCount":20}
	}`
	out, err := JSONToOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	usage, _ := got["usage"].(map[string]any)
	// completion_tokens = candidates + thoughts = 4 + 6 = 10
	if usage["completion_tokens"].(float64) != 10 {
		t.Errorf("completion_tokens=%v want 10", usage["completion_tokens"])
	}
	details, _ := usage["completion_tokens_details"].(map[string]any)
	if details["reasoning_tokens"].(float64) != 6 {
		t.Errorf("reasoning_tokens=%v want 6", details["reasoning_tokens"])
	}
}

func TestJSONToOpenAI_UsageWithCached(t *testing.T) {
	in := `{
		"candidates":[{"content":{"parts":[{"text":"x"}],"role":"model"},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"cachedContentTokenCount":3,"totalTokenCount":14}
	}`
	out, err := JSONToOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	usage, _ := got["usage"].(map[string]any)
	if usage["prompt_tokens"].(float64) != 10 {
		t.Errorf("prompt_tokens=%v want 10", usage["prompt_tokens"])
	}
	details, _ := usage["prompt_tokens_details"].(map[string]any)
	if details["cached_tokens"].(float64) != 3 {
		t.Errorf("cached_tokens=%v want 3", details["cached_tokens"])
	}
}
