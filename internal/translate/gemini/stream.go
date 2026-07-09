package gemini

import (
	"bytes"
	"encoding/json"
)

// StreamState tracks state for OpenAI → Gemini streaming translation.
type StreamState struct {
	textBuffer   string
	finishReason string
	usage        map[string]any
	closed       bool
	sseBuf       bytes.Buffer
}

// Feed appends incoming bytes and returns complete newline-terminated
// lines. Partial trailing data is retained for the next call.
func (s *StreamState) Feed(p []byte) []string {
	s.sseBuf.Write(p)
	data := s.sseBuf.String()
	var lines []string
	for {
		idx := bytes.IndexByte([]byte(data), '\n')
		if idx < 0 {
			break
		}
		lines = append(lines, data[:idx])
		data = data[idx+1:]
	}
	s.sseBuf.Reset()
	s.sseBuf.WriteString(data)
	return lines
}

// IsClosed reports whether the stream has finished.
func (s *StreamState) IsClosed() bool { return s.closed }

// ProcessChunk converts an OpenAI SSE chunk to Gemini SSE format.
// Returns the formatted SSE line(s) to send to the client, or nil to skip.
func ProcessChunk(chunk map[string]any, state *StreamState) []string {
	if state.closed {
		return nil
	}

	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return nil
	}
	choice, _ := choices[0].(map[string]any)
	if choice == nil {
		return nil
	}

	delta, _ := choice["delta"].(map[string]any)

	// Accumulate text content into the buffer (kept as the full response
	// text for the finish chunk / debugging), but emit only the new delta.
	var newText string
	if txt, ok := delta["content"].(string); ok {
		newText = txt
		state.textBuffer += txt
	}

	// Surface reasoning/thinking tokens as Gemini thought parts (kept out
	// of the visible text buffer — Gemini renders thought parts separately).
	var newThought string
	if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
		newThought = rc
	} else if r, ok := delta["reasoning"].(string); ok && r != "" {
		newThought = r
	}
	if newThought != "" {
		state.textBuffer += newThought
	}

	// Check finish reason (recorded; only emitted on the final chunk).
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" && fr != "null" {
		state.finishReason = fr
	}

	// Usage
	if usage, ok := chunk["usage"].(map[string]any); ok {
		state.usage = usage
	}

	// Build Gemini-format candidate for THIS chunk. The client expects
	// incremental parts (text and/or thought), so we emit only the new
	// deltas — not the accumulated buffer. finishReason is emitted only on
	// the terminal chunk, so intermediate chunks carry no finishReason.
	parts := []any{}
	if newThought != "" {
		parts = append(parts, map[string]any{"text": newThought, "thought": true})
	}
	if newText != "" {
		parts = append(parts, map[string]any{"text": newText})
	}
	if len(parts) == 0 && state.finishReason == "" {
		// No content and not finished yet — nothing to emit this chunk.
		return nil
	}

	candidate := map[string]any{
		"index": 0,
		"content": map[string]any{
			"parts": parts,
			"role":  "model",
		},
	}
	if state.finishReason != "" {
		candidate["finishReason"] = MapFinishReasonGemini(state.finishReason)
	}

	geminiChunk := map[string]any{
		"candidates": []any{candidate},
	}

	// Attach usage metadata if present
	if state.usage != nil {
		geminiChunk["usageMetadata"] = map[string]any{
			"promptTokenCount":     state.usage["prompt_tokens"],
			"candidatesTokenCount": state.usage["completion_tokens"],
		}
	}

	// If finished, mark as closed
	if state.finishReason != "" {
		state.closed = true
	}

	data, err := json.Marshal(geminiChunk)
	if err != nil {
		return nil
	}

	return []string{"data: " + string(data) + "\n\n"}
}

// NewStreamState creates a new Gemini streaming state.
func NewStreamState() *StreamState {
	return &StreamState{}
}

// --- Legacy aliases for backward compatibility with internal callers
// that pre-date the public streaming API. ---

// geminiStreamState is the legacy name; new code should use StreamState.
type geminiStreamState = StreamState

// processGeminiChunk is the legacy name; new code should use ProcessChunk.
func processGeminiChunk(chunk map[string]any, state *StreamState) []string {
	return ProcessChunk(chunk, state)
}

// NewGeminiStreamState is the legacy name; new code should use NewStreamState.
func NewGeminiStreamState() *StreamState {
	return NewStreamState()
}
