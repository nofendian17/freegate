package translate

// ResponseJSON translates a non-streaming OpenAI response body to the target format.
func ResponseJSON(body []byte, target Format) ([]byte, error) {
	switch target {
	case FormatClaude:
		return openaiJSONToClaude(body)
	case FormatGemini:
		return openaiJSONToGemini(body)
	default:
		return body, nil
	}
}
