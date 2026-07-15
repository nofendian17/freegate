package claude

import (
	"encoding/json"
	"testing"
)

func TestJSONToOpenAI_TextOnly(t *testing.T) {
	in := `{
		"id":"msg_abc",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet-4",
		"content":[{"type":"text","text":"hello"}],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":10,"output_tokens":5}
	}`
	out, err := JSONToOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["model"] != "claude-sonnet-4" {
		t.Errorf("model=%v want claude-sonnet-4", got["model"])
	}
	choices, _ := got["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices=%v", choices)
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
	if usage["prompt_tokens"].(float64) != 10 {
		t.Errorf("prompt_tokens=%v want 10", usage["prompt_tokens"])
	}
	if usage["completion_tokens"].(float64) != 5 {
		t.Errorf("completion_tokens=%v want 5", usage["completion_tokens"])
	}
}

func TestJSONToOpenAI_ToolUse(t *testing.T) {
	in := `{
		"id":"msg_abc",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet-4",
		"content":[
			{"type":"text","text":"I'll call a tool"},
			{"type":"tool_use","id":"toolu_xyz","name":"get_weather","input":{"city":"SF"}}
		],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":10,"output_tokens":20}
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
	c0, _ := choices[0].(map[string]any)
	if c0["finish_reason"] != "tool_calls" {
		t.Errorf("finish_reason=%v want tool_calls", c0["finish_reason"])
	}
	msg, _ := c0["message"].(map[string]any)
	tcs, _ := msg["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(tcs))
	}
	tc, _ := tcs[0].(map[string]any)
	if tc["id"] != "toolu_xyz" {
		t.Errorf("id=%v want toolu_xyz", tc["id"])
	}
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

func TestJSONToOpenAI_StopReason_MaxTokens(t *testing.T) {
	in := `{
		"id":"msg_abc",
		"role":"assistant",
		"content":[{"type":"text","text":"..."}],
		"stop_reason":"max_tokens"
	}`
	out, err := JSONToOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	choices, _ := got["choices"].([]any)
	c0, _ := choices[0].(map[string]any)
	if c0["finish_reason"] != "length" {
		t.Errorf("finish_reason=%v want length", c0["finish_reason"])
	}
}

func TestJSONToOpenAI_CacheTokens(t *testing.T) {
	in := `{
		"id":"msg_abc",
		"role":"assistant",
		"content":[{"type":"text","text":"..."}],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":5,"output_tokens":3,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}
	}`
	out, err := JSONToOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	usage, _ := got["usage"].(map[string]any)
	// prompt_tokens = 5 (uncached) + 2 (cache_read) + 1 (cache_create) = 8
	if usage["prompt_tokens"].(float64) != 8 {
		t.Errorf("prompt_tokens=%v want 8", usage["prompt_tokens"])
	}
	details, _ := usage["prompt_tokens_details"].(map[string]any)
	if details["cached_tokens"].(float64) != 2 {
		t.Errorf("cached_tokens=%v want 2", details["cached_tokens"])
	}
	if details["cache_creation_tokens"].(float64) != 1 {
		t.Errorf("cache_creation_tokens=%v want 1", details["cache_creation_tokens"])
	}
}

// TestJSONToOpenAI_ToolUseInputNormalized verifies the Claude->OpenAI
// response path normalizes malformed tool_use input to a valid JSON object,
// matching the OpenAI->Claude repair. Models/clients may emit a bare string,
// array, or an __unparsedToolInput wrapper; the tool arguments must still be a
// JSON object so the downstream OpenAI client can parse it.
func TestJSONToOpenAI_ToolUseInputNormalized(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"bare-string", `{"type":"tool_use","id":"t1","name":"Bash","input":"ls -la"}`},
		{"array", `{"type":"tool_use","id":"t1","name":"Bash","input":["ls","-la"]}`},
		{"stringified-object", `{"type":"tool_use","id":"t1","name":"Bash","input":"{\"cmd\":\"ls -la\"}"}`},
		{"unparsed-wrapper", `{"type":"tool_use","id":"t1","name":"Bash","input":{"__unparsedToolInput":{"raw":"{\"cmd\":\"echo \"hi\"\"}"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := `{
				"id":"msg_abc",
				"type":"message",
				"role":"assistant",
				"content":[` + tc.input + `],
				"stop_reason":"tool_use"
			}`
			out, err := JSONToOpenAI([]byte(in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("invalid JSON output: %v", err)
			}
			choices, _ := got["choices"].([]any)
			c0, _ := choices[0].(map[string]any)
			msg, _ := c0["message"].(map[string]any)
			tcs, _ := msg["tool_calls"].([]any)
			if len(tcs) != 1 {
				t.Fatalf("expected 1 tool_call, got %d", len(tcs))
			}
			fn, _ := tcs[0].(map[string]any)["function"].(map[string]any)
			args := fn["arguments"].(string)
			var v any
			if err := json.Unmarshal([]byte(args), &v); err != nil {
				t.Fatalf("arguments not valid JSON: %q err=%v", args, err)
			}
			if _, ok := v.(map[string]any); !ok {
				t.Fatalf("arguments not a JSON object: %q (got %T)", args, v)
			}
		})
	}
}
