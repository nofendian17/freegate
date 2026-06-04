package translate

// Request translates a request body from source format to target format.
// If source == target, returns the body unchanged.
func Request(body []byte, source, target Format) ([]byte, error) {
	if source == target {
		return body, nil
	}

	// All translation goes through OpenAI intermediate format
	switch source {
	case FormatClaude:
		return claudeToOpenAI(body)
	case FormatGemini:
		return geminiToOpenAI(body)
	default:
		return body, nil
	}
}
