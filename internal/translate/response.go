package translate

import (
	"fmt"

	"freegate/internal/translate/claude"
	"freegate/internal/translate/gemini"
)

// ResponseJSON translates a non-streaming response body from source
// format to target format.
//
// Translation is two-hop via OpenAI when neither side is OpenAI:
//
//	Claude JSON → OpenAI JSON → Gemini JSON
func ResponseJSON(body []byte, source, target Format) ([]byte, error) {
	if source == target {
		return body, nil
	}

	// Step 1: source → OpenAI.
	out, err := sourceJSONToOpenAI(body, source)
	if err != nil {
		return nil, err
	}

	// Step 2: OpenAI → target.
	out, err = openAIJSONToTarget(out, target)
	if err != nil {
		return nil, err
	}

	return out, nil
}

func sourceJSONToOpenAI(body []byte, source Format) ([]byte, error) {
	switch source {
	case FormatClaude:
		return claude.JSONToOpenAI(body)
	case FormatGemini:
		return gemini.JSONToOpenAI(body)
	case FormatOpenAI, "":
		return body, nil
	}
	return nil, fmt.Errorf("translate: unsupported source format %q", source)
}

func openAIJSONToTarget(body []byte, target Format) ([]byte, error) {
	switch target {
	case FormatClaude:
		return claude.JSONToClaude(body)
	case FormatGemini:
		return gemini.JSONToGemini(body)
	case FormatOpenAI, "":
		return body, nil
	}
	return nil, fmt.Errorf("translate: unsupported target format %q", target)
}
