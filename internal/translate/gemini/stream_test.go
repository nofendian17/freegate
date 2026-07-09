package gemini

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- OpenAI → Gemini stream tests (existing direction) ---

// TestProcessGeminiChunk_FinishOnlyOnLast asserts that finishReason is
// emitted only on the terminal chunk, not duplicated on every chunk.
func TestProcessGeminiChunk_FinishOnlyOnLast(t *testing.T) {
	state := NewGeminiStreamState()
	mid := map[string]any{
		"choices": []any{
			map[string]any{"index": 0.0, "delta": map[string]any{"content": "hi"}},
		},
	}
	if events := processGeminiChunk(mid, state); events == nil {
		t.Fatal("expected events for mid chunk")
	}
	if state.finishReason != "" {
		t.Errorf("finishReason set too early: %q", state.finishReason)
	}
	if strings.Contains(eventsText(t, processGeminiChunk(mid, state)), "STOP") {
		t.Error("finishReason must not appear on intermediate chunks")
	}

	fin := map[string]any{
		"choices": []any{
			map[string]any{"index": 0.0, "delta": map[string]any{}, "finish_reason": "stop"},
		},
	}
	last := processGeminiChunk(fin, state)
	if !strings.Contains(eventsText(t, last), "STOP") {
		t.Error("expected STOP on final chunk")
	}
	if !state.closed {
		t.Error("expected state closed after finish")
	}
}

// TestProcessGeminiChunk_ReasoningAsThought asserts reasoning_content is
// surfaced to Gemini clients as a thought part, not dropped.
func TestProcessGeminiChunk_ReasoningAsThought(t *testing.T) {
	state := NewGeminiStreamState()
	chunk := map[string]any{
		"choices": []any{
			map[string]any{"index": 0.0, "delta": map[string]any{"reasoning_content": "thinking..."}},
		},
	}
	events := processGeminiChunk(chunk, state)
	text := eventsText(t, events)
	if !strings.Contains(text, "thinking...") {
		t.Errorf("reasoning not surfaced; got %s", text)
	}
	if !strings.Contains(text, `"thought":true`) {
		t.Errorf("reasoning should be a thought part; got %s", text)
	}
}

func eventsText(t *testing.T, events []string) string {
	t.Helper()
	var sb strings.Builder
	for _, e := range events {
		sb.WriteString(e)
	}
	return sb.String()
}


// must emit only its own delta, not the full accumulated textBuffer.
// Emitting the whole buffer on every chunk corrupts Gemini streaming output
// and makes the stream O(n^2).
func TestProcessGeminiChunk_IncrementalText(t *testing.T) {
	state := NewGeminiStreamState()

	first := map[string]any{
		"choices": []any{
			map[string]any{"index": 0.0, "delta": map[string]any{"content": "Hello "}},
		},
	}
	events1 := processGeminiChunk(first, state)
	got1 := firstDataText(t, events1)
	if got1 != "Hello " {
		t.Errorf("chunk 1 emitted text=%q, want %q", got1, "Hello ")
	}

	second := map[string]any{
		"choices": []any{
			map[string]any{"index": 0.0, "delta": map[string]any{"content": "world"}},
		},
	}
	events2 := processGeminiChunk(second, state)
	got2 := firstDataText(t, events2)
	if got2 != "world" {
		t.Errorf("chunk 2 emitted text=%q, want %q (must be the delta, not the full buffer)", got2, "world")
	}

	// textBuffer still accumulates the full response.
	if state.textBuffer != "Hello world" {
		t.Errorf("textBuffer=%q want %q", state.textBuffer, "Hello world")
	}
}

func firstDataText(t *testing.T, events []string) string {
	t.Helper()
	for _, e := range events {
		if !strings.HasPrefix(e, "data: ") {
			continue
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(e, "data: ")), &got); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		cands, _ := got["candidates"].([]any)
		if len(cands) == 0 {
			continue
		}
		c0, _ := cands[0].(map[string]any)
		content, _ := c0["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		if len(parts) == 0 {
			continue
		}
		p, _ := parts[0].(map[string]any)
		if txt, _ := p["text"].(string); txt != "" {
			return txt
		}
	}
	return ""
}

func TestProcessGeminiChunk_Text(t *testing.T) {
	state := NewGeminiStreamState()
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
		t.Errorf("textBuffer=%q want %q", state.textBuffer, "Hello ")
	}
}

func TestProcessGeminiChunk_Finish(t *testing.T) {
	state := NewGeminiStreamState()
	chunk := map[string]any{
		"choices": []any{
			map[string]any{
				"index":         0.0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens": 5.0, "completion_tokens": 3.0,
		},
	}
	events := processGeminiChunk(chunk, state)
	if !state.closed {
		t.Error("expected state to be closed")
	}
	// Last event should carry finishReason=STOP.
	var lastData string
	for _, e := range events {
		if strings.HasPrefix(e, "data: ") {
			lastData = e[6:]
		}
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lastData), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	cands, _ := got["candidates"].([]any)
	c0, _ := cands[0].(map[string]any)
	if c0["finishReason"] != "STOP" {
		t.Errorf("finishReason=%v want STOP", c0["finishReason"])
	}
	if _, ok := got["usageMetadata"]; !ok {
		t.Errorf("expected usageMetadata in final chunk")
	}
}

func TestProcessGeminiChunk_EmptyChunk(t *testing.T) {
	state := NewGeminiStreamState()
	if events := processGeminiChunk(map[string]any{}, state); events != nil {
		t.Errorf("expected nil for empty chunk, got %v", events)
	}
}

// --- Gemini → OpenAI stream tests ---

func TestGeminiToOpenAIStream_Text(t *testing.T) {
	state := NewGeminiToOpenAIState()
	chunk := map[string]any{
		"candidates": []any{
			map[string]any{
				"index": 0.0,
				"content": map[string]any{
					"parts": []any{
						map[string]any{"text": "Hello "},
					},
					"role": "model",
				},
			},
		},
	}
	events := state.ProcessChunk(chunk)
	if len(events) == 0 {
		t.Fatal("expected events")
	}
	// Each event should be a data: <json>\n\n SSE line.
	for _, e := range events {
		if !strings.HasPrefix(e, "data: ") {
			t.Errorf("event %q is not data-prefixed", e)
		}
		if !strings.HasSuffix(e, "\n\n") {
			t.Errorf("event %q is not double-newline-terminated", e)
		}
	}
	// Parse the first event and check content.
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(events[0], "data: ")), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	choices := got["choices"].([]any)
	c0 := choices[0].(map[string]any)
	delta := c0["delta"].(map[string]any)
	if delta["content"] != "Hello " {
		t.Errorf("content=%v want %q", delta["content"], "Hello ")
	}
}

func TestGeminiToOpenAIStream_Thought(t *testing.T) {
	state := NewGeminiToOpenAIState()
	chunk := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						map[string]any{"text": "thinking...", "thought": true},
					},
					"role": "model",
				},
			},
		},
	}
	events := state.ProcessChunk(chunk)
	if len(events) == 0 {
		t.Fatal("expected events for thought part")
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(strings.TrimPrefix(events[0], "data: ")), &got)
	delta := got["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["reasoning_content"] != "thinking..." {
		t.Errorf("expected reasoning_content=thinking..., got %+v", delta)
	}
}

func TestGeminiToOpenAIStream_FunctionCall(t *testing.T) {
	state := NewGeminiToOpenAIState()
	chunk := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						map[string]any{
							"functionCall": map[string]any{
								"name": "get_weather",
								"args": map[string]any{"city": "SF"},
							},
						},
					},
					"role": "model",
				},
			},
		},
	}
	events := state.ProcessChunk(chunk)
	if len(events) == 0 {
		t.Fatal("expected events for functionCall part")
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(strings.TrimPrefix(events[0], "data: ")), &got)
	delta := got["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	tcs, _ := delta["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(tcs))
	}
	tc := tcs[0].(map[string]any)
	fn := tc["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("name=%v want get_weather", fn["name"])
	}
}

func TestGeminiToOpenAIStream_FinishReason(t *testing.T) {
	state := NewGeminiToOpenAIState()
	chunk := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{map[string]any{"text": "bye"}},
					"role":  "model",
				},
				"finishReason": "STOP",
			},
		},
		"usageMetadata": map[string]any{
			"promptTokenCount":     5.0,
			"candidatesTokenCount": 2.0,
			"totalTokenCount":      7.0,
		},
	}
	events := state.ProcessChunk(chunk)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (text + finish), got %d", len(events))
	}
	if !state.IsClosed() {
		t.Error("expected state to be closed after finish")
	}
	// Last event should have finish_reason=stop and usage attached.
	last := events[len(events)-1]
	var got map[string]any
	_ = json.Unmarshal([]byte(strings.TrimPrefix(last, "data: ")), &got)
	choice := got["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason=%v want stop", choice["finish_reason"])
	}
	usage, ok := got["usage"].(map[string]any)
	if !ok {
		t.Fatal("expected usage on final chunk")
	}
	if usage["prompt_tokens"].(float64) != 5 {
		t.Errorf("prompt_tokens=%v want 5", usage["prompt_tokens"])
	}
	if usage["completion_tokens"].(float64) != 2 {
		t.Errorf("completion_tokens=%v want 2", usage["completion_tokens"])
	}
}

func TestGeminiToOpenAIStream_FinishReasonLength(t *testing.T) {
	state := NewGeminiToOpenAIState()
	chunk := map[string]any{
		"candidates": []any{
			map[string]any{
				"content":      map[string]any{"parts": []any{}, "role": "model"},
				"finishReason": "MAX_TOKENS",
			},
		},
	}
	state.ProcessChunk(chunk)
	// Now trigger another chunk to emit the final.
	state.ProcessChunk(map[string]any{
		"candidates": []any{
			map[string]any{
				"content":      map[string]any{"parts": []any{}, "role": "model"},
				"finishReason": "MAX_TOKENS",
			},
		},
	})
	// (We just want to ensure the finishReason was mapped; the second
	// call hits a closed state and emits nothing.) Re-test on a fresh
	// state with the chunk being the only one.
	state2 := NewGeminiToOpenAIState()
	events := state2.ProcessChunk(chunk)
	if len(events) == 0 {
		t.Fatal("expected events")
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(strings.TrimPrefix(events[len(events)-1], "data: ")), &got)
	choice := got["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "length" {
		t.Errorf("finish_reason=%v want length", choice["finish_reason"])
	}
}

func TestGeminiToOpenAIStream_UsageWithThinking(t *testing.T) {
	state := NewGeminiToOpenAIState()
	chunk := map[string]any{
		"candidates": []any{
			map[string]any{
				"content":      map[string]any{"parts": []any{}, "role": "model"},
				"finishReason": "STOP",
			},
		},
		"usageMetadata": map[string]any{
			"promptTokenCount":     10.0,
			"candidatesTokenCount": 4.0,
			"thoughtsTokenCount":   6.0,
			"totalTokenCount":      20.0,
		},
	}
	events := state.ProcessChunk(chunk)
	if len(events) == 0 {
		t.Fatal("expected events")
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(strings.TrimPrefix(events[len(events)-1], "data: ")), &got)
	usage := got["usage"].(map[string]any)
	// completion_tokens = candidates + thoughts = 4 + 6 = 10
	if usage["completion_tokens"].(float64) != 10 {
		t.Errorf("completion_tokens=%v want 10", usage["completion_tokens"])
	}
	details, _ := usage["completion_tokens_details"].(map[string]any)
	if details["reasoning_tokens"].(float64) != 6 {
		t.Errorf("reasoning_tokens=%v want 6", details["reasoning_tokens"])
	}
}

func TestGeminiToOpenAIStream_UsageWithCached(t *testing.T) {
	state := NewGeminiToOpenAIState()
	chunk := map[string]any{
		"candidates": []any{
			map[string]any{
				"content":      map[string]any{"parts": []any{}, "role": "model"},
				"finishReason": "STOP",
			},
		},
		"usageMetadata": map[string]any{
			"promptTokenCount":        10.0,
			"candidatesTokenCount":    4.0,
			"cachedContentTokenCount": 3.0,
			"totalTokenCount":         14.0,
		},
	}
	events := state.ProcessChunk(chunk)
	if len(events) == 0 {
		t.Fatal("expected events")
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(strings.TrimPrefix(events[len(events)-1], "data: ")), &got)
	usage := got["usage"].(map[string]any)
	details, _ := usage["prompt_tokens_details"].(map[string]any)
	if details["cached_tokens"].(float64) != 3 {
		t.Errorf("cached_tokens=%v want 3", details["cached_tokens"])
	}
}

func TestGeminiToOpenAIStream_ClosedNoEmit(t *testing.T) {
	state := NewGeminiToOpenAIState()
	// First chunk finishes the stream.
	state.ProcessChunk(map[string]any{
		"candidates": []any{
			map[string]any{
				"content":      map[string]any{"parts": []any{}, "role": "model"},
				"finishReason": "STOP",
			},
		},
	})
	// Subsequent chunks should emit nothing.
	events := state.ProcessChunk(map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{map[string]any{"text": "should be dropped"}},
					"role":  "model",
				},
			},
		},
	})
	if len(events) != 0 {
		t.Errorf("expected no events from closed state, got %d", len(events))
	}
}

func TestGeminiToOpenAIStream_FeedBuffersPartialLines(t *testing.T) {
	state := NewGeminiToOpenAIState()
	// Feed one byte at a time to verify the buffer.
	payload := `{"candidates":[{"content":{"parts":[{"text":"x"}],"role":"model"},"finishReason":"STOP"}]}`
	var all []string
	for i := 0; i < len(payload); i++ {
		all = append(all, state.Feed([]byte{payload[i]})...)
	}
	// Append a final newline so the line is "complete".
	all = append(all, state.Feed([]byte("\n"))...)
	if len(all) == 0 {
		t.Fatal("expected at least one line from Feed")
	}
}

// --- Helpers ---

func TestParseGeminiUsage(t *testing.T) {
	um := map[string]any{
		"promptTokenCount":        float64(7),
		"candidatesTokenCount":    float64(3),
		"thoughtsTokenCount":      float64(2),
		"cachedContentTokenCount": float64(1),
	}
	u := parseGeminiUsage(um)
	if u.PromptTokens != 7 {
		t.Errorf("PromptTokens=%d want 7", u.PromptTokens)
	}
	if u.CandidatesTokens != 3 {
		t.Errorf("CandidatesTokens=%d want 3", u.CandidatesTokens)
	}
	if u.ThoughtsTokens != 2 {
		t.Errorf("ThoughtsTokens=%d want 2", u.ThoughtsTokens)
	}
	if u.CachedTokens != 1 {
		t.Errorf("CachedTokens=%d want 1", u.CachedTokens)
	}
}
