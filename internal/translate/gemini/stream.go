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

	// Accumulate text content
	if txt, ok := delta["content"].(string); ok {
		state.textBuffer += txt
	}

	// Check finish reason
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" && fr != "null" {
		state.finishReason = fr
	}

	// Usage
	if usage, ok := chunk["usage"].(map[string]any); ok {
		state.usage = usage
	}

	// Build Gemini-format candidate
	candidate := map[string]any{
		"index":        0,
		"finishReason": MapFinishReasonGemini(state.finishReason),
		"content": map[string]any{
			"parts": []any{
				map[string]any{"text": state.textBuffer},
			},
			"role": "model",
		},
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
