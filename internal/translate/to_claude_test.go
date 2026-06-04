package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProcessOpenAIChunkBasic(t *testing.T) {
	state := newClaudeStream()

	// Chunk 1: first delta with content
	chunk := map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0.0,
				"delta": map[string]any{
					"role":    "assistant",
					"content": "Hello",
				},
			},
		},
	}
	events := processOpenAIChunk(chunk, state)
	if !state.messageStartSent {
		t.Error("expected message_start to be sent")
	}

	// Should have message_start + content_block_start + content_block_delta
	hasStart := false
	hasBlockStart := false
	for _, e := range events {
		if strings.Contains(e, "message_start") {
			hasStart = true
		}
		if strings.Contains(e, "content_block_start") {
			hasBlockStart = true
		}
	}
	if !hasStart {
		t.Error("expected message_start event")
	}
	if !hasBlockStart {
		t.Error("expected content_block_start event")
	}
}

func TestProcessOpenAIChunkReasoning(t *testing.T) {
	state := newClaudeStream()

	// Reasoning content
	chunk := map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0.0,
				"delta": map[string]any{
					"reasoning_content": "thinking step by step",
				},
			},
		},
	}
	events := processOpenAIChunk(chunk, state)

	hasThinking := false
	for _, e := range events {
		if strings.Contains(e, `"type":"thinking"`) || strings.Contains(e, "thinking_delta") {
			hasThinking = true
		}
	}
	if !hasThinking {
		t.Error("expected thinking block events")
	}
}

func TestProcessOpenAIChunkFinishStop(t *testing.T) {
	state := newClaudeStream()

	// First a content chunk to set up state
	chunk1 := map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0.0,
				"delta": map[string]any{
					"content": "hi",
				},
			},
		},
	}
	processOpenAIChunk(chunk1, state)

	// Finish chunk
	chunk2 := map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0.0,
				"delta": map[string]any{},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     10.0,
			"completion_tokens": 5.0,
		},
	}
	events := processOpenAIChunk(chunk2, state)

	hasMessageDelta := false
	hasMessageStop := false
	hasUsage := false
	for _, e := range events {
		if strings.Contains(e, "message_delta") {
			hasMessageDelta = true
		}
		if strings.Contains(e, "message_stop") {
			hasMessageStop = true
		}
		if strings.Contains(e, "output_tokens") {
			hasUsage = true
		}
	}
	if !hasMessageDelta {
		t.Error("expected message_delta event")
	}
	if !hasMessageStop {
		t.Error("expected message_stop event")
	}
	if !hasUsage {
		t.Error("expected usage in message_delta")
	}

	// Verify stop_reason mapping
	if state.finishReason != "stop" {
		t.Errorf("expected finish_reason=stop, got %s", state.finishReason)
	}
}

func TestProcessOpenAIChunkToolCalls(t *testing.T) {
	state := newClaudeStream()

	// Tool call chunk
	chunk := map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0.0,
				"delta": map[string]any{
					"tool_calls": []any{
						map[string]any{
							"index": 0.0,
							"id":    "call_abc123",
							"type":  "function",
							"function": map[string]any{
								"name":      "get_weather",
								"arguments": `{"city":"NYC"}`,
							},
						},
					},
				},
			},
		},
	}
	events := processOpenAIChunk(chunk, state)

	hasToolUse := false
	for _, e := range events {
		if strings.Contains(e, "tool_use") {
			hasToolUse = true
		}
	}
	// Should have content_block_start with type=tool_use
	if !hasToolUse {
		t.Error("expected tool_use content block")
	}
	_ = events
}

func TestProcessOpenAIChunkFinishToolCalls(t *testing.T) {
	state := newClaudeStream()

	// Content
	processOpenAIChunk(map[string]any{
		"choices": []any{map[string]any{"index": 0.0, "delta": map[string]any{"content": "Let me check"}}},
	}, state)

	// Tool call
	processOpenAIChunk(map[string]any{
		"choices": []any{map[string]any{"index": 0.0, "delta": map[string]any{
			"tool_calls": []any{map[string]any{
				"index": 0.0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "search", "arguments": `{"q":"test"}`},
			}},
		}}},
	}, state)

	events := processOpenAIChunk(map[string]any{
		"choices": []any{map[string]any{"index": 0.0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
	}, state)

	hasStop := false
	hasMsgDelta := false
	hasToolUseReason := false
	for _, e := range events {
		if strings.Contains(e, "content_block_stop") {
			hasStop = true
		}
		if strings.Contains(e, "message_delta") {
			hasMsgDelta = true
		}
		if strings.Contains(e, `"stop_reason":"tool_use"`) {
			hasToolUseReason = true
		}
	}
	if !hasStop {
		t.Error("expected content_block_stop event")
	}
	if !hasMsgDelta {
		t.Error("expected message_delta event")
	}
	if !hasToolUseReason {
		t.Error("expected stop_reason=tool_use in message_delta data")
	}
}

func TestProcessOpenAIChunkStreamEndToEnd(t *testing.T) {
	state := newClaudeStream()

	// Simulate full stream: message_start → content → finish
	inputs := []string{
		`{"choices":[{"index":0,"delta":{"content":"Hello world"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`,
	}

	var allEvents []string
	for _, input := range inputs {
		var chunk map[string]any
		json.Unmarshal([]byte(input), &chunk)
		events := processOpenAIChunk(chunk, state)
		allEvents = append(allEvents, events...)
	}

	eventText := strings.Join(allEvents, " ")
	if !strings.Contains(eventText, "message_start") {
		t.Error("expected message_start")
	}
	if !strings.Contains(eventText, "message_delta") {
		t.Error("expected message_delta")
	}
	if !strings.Contains(eventText, "message_stop") {
		t.Error("expected message_stop")
	}
	if !strings.Contains(eventText, "content_block_delta") {
		t.Error("expected content_block_delta")
	}
	if !strings.Contains(eventText, "content_block_stop") {
		t.Error("expected content_block_stop")
	}
	if state.usage == nil || state.usage.InputTokens != 10 {
		t.Errorf("expected input_tokens=10, got %+v", state.usage)
	}
}

func TestMapFinishReason(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"tool_calls", "tool_use"},
		{"content_filter", "end_turn"},
		{"unknown", "end_turn"},
	}
	for _, tt := range tests {
		got := mapFinishReason(tt.input)
		if got != tt.want {
			t.Errorf("mapFinishReason(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestOpenAIJSONToClaude(t *testing.T) {
	body := `{
		"id":"chatcmpl-123",
		"model":"gpt-4",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Hello world"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":3}
	}`

	result, err := openaiJSONToClaude([]byte(body))
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

func TestOpenAIJSONToClaudeWithTools(t *testing.T) {
	body := `{
		"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":5,"completion_tokens":10}
	}`

	result, err := openaiJSONToClaude([]byte(body))
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

func TestRandID(t *testing.T) {
	id1 := randID(8)
	id2 := randID(8)
	if len(id1) != 8 {
		t.Errorf("expected length 8, got %d", len(id1))
	}
	if id1 == id2 {
		t.Error("expected random IDs to differ")
	}
}

func TestOpenAIJSONToGemini(t *testing.T) {
	body := `{
		"choices":[{"index":0,"message":{"role":"assistant","content":"Hello world"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":3}
	}`

	result, err := openaiJSONToGemini([]byte(body))
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
		got := mapFinishReasonGemini(tt.input)
		if got != tt.want {
			t.Errorf("mapFinishReasonGemini(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
