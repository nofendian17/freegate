package gemini

import (
	"encoding/json"
)

// JSONToGemini converts an OpenAI-format JSON response to Gemini format.
func JSONToGemini(body []byte) ([]byte, error) {
	var openaiResp map[string]any
	if err := json.Unmarshal(body, &openaiResp); err != nil {
		return nil, err
	}

	gemini := map[string]any{
		"candidates": []any{},
	}

	if choices, ok := openaiResp["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		if choice != nil {
			candidate := map[string]any{
				"index":        0,
				"finishReason": MapFinishReasonGemini(choice["finish_reason"]),
				"content": map[string]any{
					"parts": []any{},
					"role":  "model",
				},
			}

			if msg, ok := choice["message"].(map[string]any); ok {
				if txt, ok := msg["content"].(string); ok && txt != "" {
					candidate["content"].(map[string]any)["parts"] = []any{
						map[string]any{"text": txt},
					}
				}
			}

			gemini["candidates"] = []any{candidate}
		}
	}

	// Usage metadata
	if usage, ok := openaiResp["usage"].(map[string]any); ok {
		gemini["usageMetadata"] = map[string]any{
			"promptTokenCount":     usage["prompt_tokens"],
			"candidatesTokenCount": usage["completion_tokens"],
		}
	}

	return json.Marshal(gemini)
}

// MapFinishReasonGemini maps an OpenAI finish_reason to a Gemini finishReason.
func MapFinishReasonGemini(reason any) string {
	r, _ := reason.(string)
	switch r {
	case "stop":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "BLOCKED"
	default:
		return "STOP"
	}
}
