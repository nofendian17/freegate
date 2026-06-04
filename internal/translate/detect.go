package translate

import (
	"encoding/json"
	"strings"
)

// Detect inspects a raw JSON body and returns the detected API format.
// Detection is based on structural hints in the body — no endpoint path needed.
//
// Priority:
//  1. Gemini: top-level "contents" (array) without "messages"
//  2. Claude: "messages" present AND Claude-specific fields found
//  3. OpenAI (default): everything else
func Detect(body []byte) Format {
	if len(body) == 0 {
		return FormatOpenAI
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return FormatOpenAI
	}

	// Gemini: top-level "contents" as an array (and no "messages")
	if _, hasContents := raw["contents"]; hasContents {
		if _, hasMessages := raw["messages"]; !hasMessages {
			if contents, ok := raw["contents"].([]any); ok && len(contents) > 0 {
				return FormatGemini
			}
		}
	}

	// Claude: "messages" with Claude-specific indicators
	if msgs, ok := raw["messages"].([]any); ok && len(msgs) > 0 {
		// Explicit Claude fields
		if _, ok := raw["anthropic_version"]; ok {
			return FormatClaude
		}
		if _, ok := raw["max_tokens"]; ok {
			// max_tokens at top level = very likely Claude
			if _, ok := raw["max_tokens"].(float64); ok {
				return FormatClaude
			}
		}

		// Claude system prompt at top level (string or array of {type:"text"})
		if sys, ok := raw["system"]; ok {
			switch sys.(type) {
			case string:
				return FormatClaude
			case []any:
				return FormatClaude
			}
		}

		// Check message content blocks for Claude-specific types
		if hasClaudeContentTypes(msgs) {
			return FormatClaude
		}
	}

	return FormatOpenAI
}

// hasClaudeContentTypes checks if any message has content blocks with
// Claude-specific types: image (with source.type="base64"), tool_use, tool_result.
func hasClaudeContentTypes(msgs []any) bool {
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"]
		if !ok {
			continue
		}
		switch c := content.(type) {
		case []any:
			for _, part := range c {
				block, ok := part.(map[string]any)
				if !ok {
					continue
				}
				typ, _ := block["type"].(string)
				switch typ {
				case "tool_use", "tool_result":
					return true
				case "image":
					if src, ok := block["source"].(map[string]any); ok {
						if st, _ := src["type"].(string); st == "base64" {
							return true
						}
					}
				}
			}
		case string:
			// plain string content could be either format; continue
		}
	}
	return false
}

// IsStreaming returns true if the body indicates streaming mode.
func IsStreaming(body []byte, format Format) bool {
	if len(body) == 0 {
		return false
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	switch format {
	case FormatOpenAI:
		if s, ok := raw["stream"].(bool); ok {
			return s
		}
	case FormatClaude:
		if s, ok := raw["stream"].(bool); ok {
			return s
		}
		// Non-streaming default for Claude
		return false
	case FormatGemini:
		return false
	}
	return false
}

// ExtractModelID extracts the "model" field from a body (works for OpenAI and Claude).
// Returns empty string if not found.
func ExtractModelID(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	if m, ok := raw["model"].(string); ok {
		return m
	}
	return ""
}

// SSE helper: isLineData returns true if line is an SSE data line.
func isLineData(line string) bool {
	return strings.HasPrefix(line, "data: ")
}

// SSE helper: extractData trims the "data: " prefix and trailing newlines.
func extractData(line string) string {
	s := strings.TrimPrefix(line, "data: ")
	return strings.TrimRight(s, "\r\n")
}
