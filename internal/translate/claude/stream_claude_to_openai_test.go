package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

// parseSSELines splits a string of SSE records into individual
// "data: ..." lines, dropping blank separators.
func parseSSELines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, rec := range strings.Split(s, "\n\n") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		var dataLine string
		for _, line := range strings.Split(rec, "\n") {
			if strings.HasPrefix(line, "data: ") {
				dataLine = strings.TrimPrefix(line, "data: ")
				break
			}
		}
		if dataLine == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(dataLine), &m); err != nil {
			t.Fatalf("invalid SSE data: %v (line=%q)", err, dataLine)
		}
		out = append(out, m)
	}
	return out
}

func TestProcessClaudeChunk_MessageStartEmitsRole(t *testing.T) {
	s := NewClaudeToOpenAIState()
	out := s.ProcessChunk(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":    "msg_test",
			"model": "claude-sonnet-4",
		},
	})
	if len(out) != 1 {
		t.Fatalf("expected 1 line, got %d", len(out))
	}
	parsed := parseSSELines(t, out[0])
	if len(parsed) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(parsed))
	}
	choices, _ := parsed[0]["choices"].([]any)
	c0, _ := choices[0].(map[string]any)
	delta, _ := c0["delta"].(map[string]any)
	if delta["role"] != "assistant" {
		t.Errorf("delta.role=%v want assistant", delta["role"])
	}
}

func TestProcessClaudeChunk_TextDelta(t *testing.T) {
	s := NewClaudeToOpenAIState()
	// start the text block
	_ = s.ProcessChunk(map[string]any{
		"type":          "content_block_start",
		"index":         float64(0),
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	out := s.ProcessChunk(map[string]any{
		"type":  "content_block_delta",
		"index": float64(0),
		"delta": map[string]any{"type": "text_delta", "text": "hello"},
	})
	parsed := parseSSELines(t, out[0])
	choices, _ := parsed[0]["choices"].([]any)
	delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "hello" {
		t.Errorf("content=%v want hello", delta["content"])
	}
}

func TestProcessClaudeChunk_ThinkingDelta(t *testing.T) {
	s := NewClaudeToOpenAIState()
	// open thinking block
	out := s.ProcessChunk(map[string]any{
		"type":          "content_block_start",
		"index":         float64(0),
		"content_block": map[string]any{"type": "thinking", "thinking": ""},
	})
	if len(out) != 1 {
		t.Fatalf("thinking_start should emit <think> marker, got %d", len(out))
	}
	parsed := parseSSELines(t, out[0])
	delta, _ := parsed[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "<think>" {
		t.Errorf("content=%v want <think>", delta["content"])
	}
	// reasoning_content delta
	out = s.ProcessChunk(map[string]any{
		"type":  "content_block_delta",
		"index": float64(0),
		"delta": map[string]any{"type": "thinking_delta", "thinking": "hmm"},
	})
	parsed = parseSSELines(t, out[0])
	delta, _ = parsed[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["reasoning_content"] != "hmm" {
		t.Errorf("reasoning_content=%v want hmm", delta["reasoning_content"])
	}
	// close thinking block — should emit </think>
	out = s.ProcessChunk(map[string]any{
		"type":  "content_block_stop",
		"index": float64(0),
	})
	if len(out) != 1 {
		t.Fatalf("content_block_stop should emit </think>, got %d", len(out))
	}
	parsed = parseSSELines(t, out[0])
	delta, _ = parsed[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "</think>" {
		t.Errorf("content=%v want </think>", delta["content"])
	}
}

func TestProcessClaudeChunk_ToolUseStreamed(t *testing.T) {
	s := NewClaudeToOpenAIState()
	// start
	out := s.ProcessChunk(map[string]any{
		"type":  "content_block_start",
		"index": float64(0),
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    "toolu_abc",
			"name":  "get_weather",
			"input": map[string]any{},
		},
	})
	parsed := parseSSELines(t, out[0])
	tcs, _ := parsed[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(tcs))
	}
	tc, _ := tcs[0].(map[string]any)
	if tc["id"] != "toolu_abc" {
		t.Errorf("id=%v want toolu_abc", tc["id"])
	}
	if tc["index"] != float64(0) {
		t.Errorf("index=%v want 0", tc["index"])
	}
	// stream args
	out = s.ProcessChunk(map[string]any{
		"type":  "content_block_delta",
		"index": float64(0),
		"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"city":`},
	})
	out = append(out, s.ProcessChunk(map[string]any{
		"type":  "content_block_delta",
		"index": float64(0),
		"delta": map[string]any{"type": "input_json_delta", "partial_json": `"SF"}`},
	})...)
	// Accumulated args should be `{"city":"SF"}`
	if s.toolCalls[0].Args.String() != `{"city":"SF"}` {
		t.Errorf("accumulated args=%q want %q", s.toolCalls[0].Args.String(), `{"city":"SF"}`)
	}
}

func TestProcessClaudeChunk_FinishOnMessageDelta(t *testing.T) {
	s := NewClaudeToOpenAIState()
	out := s.ProcessChunk(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn"},
		"usage": map[string]any{"input_tokens": float64(5), "output_tokens": float64(3)},
	})
	if len(out) != 1 {
		t.Fatalf("expected 1 line, got %d", len(out))
	}
	parsed := parseSSELines(t, out[0])
	c0 := parsed[0]["choices"].([]any)[0].(map[string]any)
	if c0["finish_reason"] != "stop" {
		t.Errorf("finish_reason=%v want stop", c0["finish_reason"])
	}
	usage, ok := parsed[0]["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage not in parsed chunk: %+v", parsed[0])
	}
	if v, ok := usage["prompt_tokens"].(float64); !ok || v != 5 {
		t.Errorf("prompt_tokens=%v (%T) want 5", usage["prompt_tokens"], usage["prompt_tokens"])
	}
	if v, ok := usage["completion_tokens"].(float64); !ok || v != 3 {
		t.Errorf("completion_tokens=%v (%T) want 3", usage["completion_tokens"], usage["completion_tokens"])
	}
}

func TestProcessClaudeChunk_FallbackOnMessageStop(t *testing.T) {
	s := NewClaudeToOpenAIState()
	// No message_delta, just message_stop
	out := s.ProcessChunk(map[string]any{"type": "message_stop"})
	if len(out) != 1 {
		t.Fatalf("expected 1 fallback line, got %d", len(out))
	}
	parsed := parseSSELines(t, out[0])
	c0 := parsed[0]["choices"].([]any)[0].(map[string]any)
	if c0["finish_reason"] != "stop" {
		t.Errorf("finish_reason=%v want stop", c0["finish_reason"])
	}
}

func TestProcessClaudeChunk_FallbackWithToolCalls(t *testing.T) {
	s := NewClaudeToOpenAIState()
	// start a tool_use
	_ = s.ProcessChunk(map[string]any{
		"type":          "content_block_start",
		"index":         float64(0),
		"content_block": map[string]any{"type": "tool_use", "id": "x", "name": "f", "input": map[string]any{}},
	})
	// message_stop with no message_delta
	out := s.ProcessChunk(map[string]any{"type": "message_stop"})
	parsed := parseSSELines(t, out[0])
	c0 := parsed[0]["choices"].([]any)[0].(map[string]any)
	if c0["finish_reason"] != "tool_calls" {
		t.Errorf("finish_reason=%v want tool_calls", c0["finish_reason"])
	}
}

func TestProcessClaudeChunk_CacheTokensUsage(t *testing.T) {
	s := NewClaudeToOpenAIState()
	out := s.ProcessChunk(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn"},
		"usage": map[string]any{
			"input_tokens":                float64(5),
			"output_tokens":               float64(3),
			"cache_read_input_tokens":     float64(2),
			"cache_creation_input_tokens": float64(1),
		},
	})
	parsed := parseSSELines(t, out[0])
	usage, ok := parsed[0]["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage not in parsed chunk: %+v", parsed[0])
	}
	// prompt_tokens = 5 + 2 + 1 = 8
	if v, ok := usage["prompt_tokens"].(float64); !ok || v != 8 {
		t.Errorf("prompt_tokens=%v want 8", usage["prompt_tokens"])
	}
	details, ok := usage["prompt_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("prompt_tokens_details missing: %+v", usage)
	}
	if v, ok := details["cached_tokens"].(float64); !ok || v != 2 {
		t.Errorf("cached_tokens=%v want 2", details["cached_tokens"])
	}
	if v, ok := details["cache_creation_tokens"].(float64); !ok || v != 1 {
		t.Errorf("cache_creation_tokens=%v want 1", details["cache_creation_tokens"])
	}
}

func TestFeedBuffersPartialLines(t *testing.T) {
	s := NewClaudeToOpenAIState()
	// First call: no complete lines (the trailing \n has not been seen).
	lines := s.Feed([]byte("data: {\"type\":"))
	if len(lines) != 0 {
		t.Fatalf("partial line should not be returned, got %d", len(lines))
	}
	// Second call completes the line. The SSE record is
	//   data: {...}\n\n
	// so Feed returns 2 lines: the data line and a trailing empty
	// separator line (matching the existing StreamState.Feed behavior).
	lines = s.Feed([]byte("\"message_start\"}\n\n"))
	dataLines := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "data: ") {
			dataLines++
		}
	}
	if dataLines != 1 {
		t.Fatalf("expected 1 data line, got %d (all lines: %v)", dataLines, lines)
	}
}
