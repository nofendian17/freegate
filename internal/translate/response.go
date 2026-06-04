package translate

import (
	"freegate/internal/translate/claude"
	"freegate/internal/translate/gemini"
)

// ResponseJSON translates a non-streaming OpenAI response body to the target format.
func ResponseJSON(body []byte, target Format) ([]byte, error) {
	switch target {
	case FormatClaude:
		return claude.JSONToClaude(body)
	case FormatGemini:
		return gemini.JSONToGemini(body)
	default:
		return body, nil
	}
}
