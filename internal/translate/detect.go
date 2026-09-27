package translate

import (
	"bytes"
	"encoding/json"
	"strings"
)

// DetectByPath returns a format hint based on the request path.
// /v1/responses is always openai-responses, /v1/messages is always claude.
func DetectByPath(path string, body []byte) Format {
	if strings.Contains(path, "/v1/responses") {
		return FormatOpenAIResponses
	}
	if strings.Contains(path, "/v1/messages") {
		return FormatClaude
	}
	return Detect(body)
}

// Detect inspects a raw JSON body and returns the detected API format.
// Detection is based on structural hints in the body — no endpoint path needed.
//
// Priority:
//  0. OpenAI Responses: top-level "input" (array or string) without "messages"
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

	// OpenAI Responses: has "input" (array or string) and no "messages"
	if inp, hasInput := raw["input"]; hasInput {
		if _, hasMessages := raw["messages"]; !hasMessages {
			switch inp.(type) {
			case string:
				return FormatOpenAIResponses
			case []any:
				return FormatOpenAIResponses
			}
		}
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

// bodyProbe extracts the model ID and format signals from a request body
// in a single JSON pass. RawMessages record key presence without decoding
// values; only genuinely ambiguous messages bodies pay for a second look.
type bodyProbe struct {
	Model            string          `json:"model"`
	Input            json.RawMessage `json:"input"`
	Messages         json.RawMessage `json:"messages"`
	Contents         json.RawMessage `json:"contents"`
	System           json.RawMessage `json:"system"`
	AnthropicVersion json.RawMessage `json:"anthropic_version"`
}

// rawArrayNonEmpty reports whether raw is a JSON array with at least one
// element. It returns -1 when raw is not an array at all, so callers can
// mirror Detect's exact "must be a non-empty []any" gates without decoding.
func rawArrayNonEmpty(raw json.RawMessage) int {
	t := bytes.TrimSpace(raw)
	if len(t) < 2 || t[0] != '[' || t[len(t)-1] != ']' {
		return -1
	}
	if len(bytes.TrimSpace(t[1:len(t)-1])) == 0 {
		return 0
	}
	return 1
}

// firstNonSpace returns the first non-whitespace byte of raw, or 0.
func firstNonSpace(raw json.RawMessage) byte {
	for _, b := range raw {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return b
		}
	}
	return 0
}

// ProbeBody extracts the model ID and detects the format in one unmarshal,
// replacing the Detect + ExtractModelID double parse on the request path.
// Semantics match Detect exactly; only messages bodies with possible Claude
// content blocks fall back to Detect for the precise block scan.
func ProbeBody(body []byte) (format Format, modelID string) {
	if len(body) == 0 {
		return FormatOpenAI, ""
	}
	var p bodyProbe
	if err := json.Unmarshal(body, &p); err != nil {
		return FormatOpenAI, ""
	}
	hasInput, hasMessages, hasContents := len(p.Input) > 0, len(p.Messages) > 0, len(p.Contents) > 0

	// OpenAI Responses: "input" (string or array) without "messages".
	if hasInput && !hasMessages {
		var v any
		if err := json.Unmarshal(p.Input, &v); err == nil {
			switch v.(type) {
			case string, []any:
				return FormatOpenAIResponses, p.Model
			}
		}
	}

	// Gemini: non-empty "contents" array without "messages".
	if hasContents && !hasMessages {
		var arr []any
		if err := json.Unmarshal(p.Contents, &arr); err == nil && len(arr) > 0 {
			return FormatGemini, p.Model
		}
	}

	// Claude needs messages as a non-empty array (mirrors Detect's gate).
	if hasMessages && rawArrayNonEmpty(p.Messages) > 0 {
		if len(p.AnthropicVersion) > 0 {
			return FormatClaude, p.Model
		}
		// Top-level system string/array. Other shapes fall through to
		// the content-block scan, exactly like Detect.
		if len(p.System) > 0 {
			if c := firstNonSpace(p.System); c == '"' || c == '[' {
				return FormatClaude, p.Model
			}
		}
		// Fast reject: no Claude block markers anywhere, skip the scan.
		if !bytes.Contains(body, []byte(`"tool_use"`)) &&
			!bytes.Contains(body, []byte(`"tool_result"`)) &&
			!bytes.Contains(body, []byte(`"base64"`)) {
			return FormatOpenAI, p.Model
		}
		return Detect(body), p.Model
	}

	return FormatOpenAI, p.Model
}

// ProbeByPath is DetectByPath + ProbeBody in a single pass: path hints win,
// otherwise the body is probed once for both format and model ID.
func ProbeByPath(path string, body []byte) (format Format, modelID string) {
	if strings.Contains(path, "/v1/responses") {
		return FormatOpenAIResponses, ExtractModelID(body)
	}
	if strings.Contains(path, "/v1/messages") {
		return FormatClaude, ExtractModelID(body)
	}
	return ProbeBody(body)
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
