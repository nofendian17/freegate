package translate

import (
	"freegate/internal/translate/claude"
	"freegate/internal/translate/gemini"
)

// Request translates a request body from source format to target format.
// If source == target, returns the body unchanged.
func Request(body []byte, source, target Format) ([]byte, error) {
	if source == target {
		return body, nil
	}

	// All translation goes through OpenAI intermediate format
	switch source {
	case FormatClaude:
		return claude.ToOpenAI(body)
	case FormatGemini:
		return gemini.ToOpenAI(body)
	default:
		return body, nil
	}
}
