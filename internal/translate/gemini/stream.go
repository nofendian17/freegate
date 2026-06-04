package gemini

import (
	"encoding/json"
)

// geminiStreamState tracks state for OpenAI → Gemini streaming translation.
type geminiStreamState struct {
	textBuffer   string
	finishReason string
	usage        map[string]any
	closed       bool
}

// processGeminiChunk converts an OpenAI SSE chunk to Gemini SSE format.
// Returns the formatted SSE line(s) to send to the client, or nil to skip.
func processGeminiChunk(chunk map[string]any, state *geminiStreamState) []string {
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
		"finishReason": mapFinishReasonGemini(state.finishReason),
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

// NewGeminiStreamState creates a new Gemini streaming state.
func NewGeminiStreamState() *geminiStreamState {
	return &geminiStreamState{}
}
