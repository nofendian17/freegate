package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProcessChunkBasic(t *testing.T) {
	state := NewStreamState()

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
	events := ProcessChunk(chunk, state)
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

func TestProcessChunkReasoning(t *testing.T) {
	state := NewStreamState()

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
	events := ProcessChunk(chunk, state)

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

func TestProcessChunkFinishStop(t *testing.T) {
	state := NewStreamState()

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
	ProcessChunk(chunk1, state)

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
	events := ProcessChunk(chunk2, state)

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

func TestProcessChunkToolCalls(t *testing.T) {
	state := NewStreamState()

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
	events := ProcessChunk(chunk, state)

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

func TestProcessChunkFinishToolCalls(t *testing.T) {
	state := NewStreamState()

	// Content
	ProcessChunk(map[string]any{
		"choices": []any{map[string]any{"index": 0.0, "delta": map[string]any{"content": "Let me check"}}},
	}, state)

	// Tool call
	ProcessChunk(map[string]any{
		"choices": []any{map[string]any{"index": 0.0, "delta": map[string]any{
			"tool_calls": []any{map[string]any{
				"index": 0.0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "search", "arguments": `{"q":"test"}`},
			}},
		}}},
	}, state)

	events := ProcessChunk(map[string]any{
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

func TestProcessChunkStreamEndToEnd(t *testing.T) {
	state := NewStreamState()

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
		events := ProcessChunk(chunk, state)
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

func TestSSEBuffer(t *testing.T) {
	sb := &sseBuffer{}
	lines := sb.Feed([]byte("data: hello\n\n"))
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0] != "data: hello" {
		t.Errorf("unexpected content: %s", lines[0])
	}
}

func TestSSEBufferPartial(t *testing.T) {
	sb := &sseBuffer{}
	// Feed incomplete message
	lines := sb.Feed([]byte("data: "))
	if len(lines) != 0 {
		t.Errorf("expected 0 lines for partial, got %d", len(lines))
	}
	// Complete it
	lines = sb.Feed([]byte("hello\n\n"))
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
}

func TestSSEBufferMultiple(t *testing.T) {
	sb := &sseBuffer{}
	lines := sb.Feed([]byte("data: a\n\ndata: b\n\n"))
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
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

// Ensure no data races by running in parallel
func TestProcessChunkConcurrent(t *testing.T) {
	t.Parallel()
	done := make(chan bool, 2)
	go func() {
		for i := 0; i < 100; i++ {
			state := NewStreamState()
			chunk := map[string]any{
				"choices": []any{
					map[string]any{
						"index": 0.0,
						"delta": map[string]any{"content": "test"},
					},
				},
			}
			ProcessChunk(chunk, state)
		}
		done <- true
	}()
	go func() {
		for i := 0; i < 100; i++ {
			state := NewStreamState()
			ProcessChunk(map[string]any{"choices": []any{map[string]any{
				"index": 0.0, "delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"index": 0.0, "id": "call_1", "type": "function",
						"function": map[string]any{"name": "test", "arguments": "{}"},
					}},
				},
			}}}, state)
		}
		done <- true
	}()
	<-done
	<-done
}

// Benchmark streaming chunk translation
func BenchmarkProcessChunk(b *testing.B) {
	state := NewStreamState()
	chunk := map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0.0,
				"delta": map[string]any{
					"role":    "assistant",
					"content": "Hello world!",
				},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ProcessChunk(chunk, state)
	}
}
