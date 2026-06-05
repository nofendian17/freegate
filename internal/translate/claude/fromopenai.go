package claude

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FromOpenAI converts an OpenAI-format chat-completions request body to
// Claude format. Mirrors 9router's request/openai-to-claude.js, minus
// the Claude-OAuth tool-name prefixing and the Claude Code system-prompt
// injection (those are specific to that project's deployment).
//
// The caller is expected to have already run prepost.* helpers on the
// OpenAI body (AdjustMaxTokens, EnsureToolCallIds, FixMissingToolResponses,
// NormalizeThinkingConfig) so the input is already normalized.
func FromOpenAI(body []byte) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("claude: invalid FromOpenAI body: %w", err)
	}

	out := map[string]any{}

	// Pass-through scalars
	if v, ok := src["model"]; ok {
		out["model"] = v
	}
	if v, ok := src["max_tokens"]; ok {
		out["max_tokens"] = v
	}
	if v, ok := src["temperature"]; ok {
		out["temperature"] = v
	}
	if v, ok := src["top_p"]; ok {
		out["top_p"] = v
	}
	if v, ok := src["stream"]; ok {
		out["stream"] = v
	}
	if v, ok := src["metadata"]; ok {
		out["metadata"] = v
	}
	if v, ok := src["stop_sequences"]; ok {
		out["stop_sequences"] = v
	}
	if v, ok := src["top_k"]; ok {
		out["top_k"] = v
	}

	// Collect system prompt: from messages[role=system] + response_format
	// (if any). Final result is a JSON array of {type, text} blocks.
	systemParts := collectSystemParts(src)
	if rf, ok := src["response_format"].(map[string]any); ok {
		if extra := convertResponseFormatToSystem(rf); extra != "" {
			systemParts = append(systemParts, extra)
		}
	}
	if len(systemParts) > 0 {
		blocks := []any{}
		for _, s := range systemParts {
			blocks = append(blocks, map[string]any{
				"type": "text",
				"text": s,
			})
		}
		out["system"] = blocks
	}

	// Build messages by walking non-system messages with a state machine
	// that merges consecutive same-role entries and flushes after each
	// tool_use (Claude requires tool_use in its own assistant message).
	out["messages"] = buildClaudeMessages(src)

	// Tools
	if tools, ok := src["tools"].([]any); ok && len(tools) > 0 {
		claudeTools := make([]any, 0, len(tools))
		for _, tAny := range tools {
			t, _ := tAny.(map[string]any)
			if t == nil {
				continue
			}
			// Skip built-in non-function tools (e.g. web_search_20250305)
			// — pass through unchanged.
			if ttype, _ := t["type"].(string); ttype != "" && ttype != "function" {
				claudeTools = append(claudeTools, t)
				continue
			}
			fn, _ := t["function"].(map[string]any)
			if fn == nil {
				fn = t // already in Claude form (name, description, input_schema)
			}
			name, _ := fn["name"].(string)
			if name == "" {
				continue
			}
			desc, _ := fn["description"].(string)
			params := fn["parameters"]
			if params == nil {
				if is, ok := t["input_schema"]; ok {
					params = is
				} else {
					params = map[string]any{"type": "object", "properties": map[string]any{}}
				}
			}
			claudeTools = append(claudeTools, map[string]any{
				"name":        name,
				"description": desc,
				"input_schema": params,
			})
		}
		if len(claudeTools) > 0 {
			out["tools"] = claudeTools
		}
	}

	// Tool choice
	if tc, ok := src["tool_choice"]; ok {
		out["tool_choice"] = convertOpenAIToolChoice(tc)
	}

	// Thinking: pass-through, else map reasoning_effort
	if th, ok := src["thinking"].(map[string]any); ok {
		out["thinking"] = th
	} else if eff, ok := src["reasoning_effort"].(string); ok && eff != "" {
		if budget := reasoningEffortToBudget(eff); budget > 0 {
			out["thinking"] = map[string]any{
				"type":         "enabled",
				"budget_tokens": budget,
			}
		}
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("claude: marshal FromOpenAI: %w", err)
	}
	return encoded, nil
}

func collectSystemParts(src map[string]any) []string {
	var parts []string
	msgs, ok := src["messages"].([]any)
	if !ok {
		return parts
	}
	for _, mAny := range msgs {
		m, _ := mAny.(map[string]any)
		if m == nil {
			continue
		}
		role, _ := m["role"].(string)
		if role != "system" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			if c != "" {
				parts = append(parts, c)
			}
		case []any:
			for _, pAny := range c {
				p, _ := pAny.(map[string]any)
				if p == nil {
					continue
				}
				if typ, _ := p["type"].(string); typ == "text" {
					if txt, _ := p["text"].(string); txt != "" {
						parts = append(parts, txt)
					}
				}
			}
		}
	}
	return parts
}

func convertResponseFormatToSystem(rf map[string]any) string {
	typ, _ := rf["type"].(string)
	switch typ {
	case "json_object":
		return "You must respond with valid JSON. Respond ONLY with a JSON object, no other text."
	case "json_schema":
		js, _ := rf["json_schema"].(map[string]any)
		schema, _ := js["schema"]
		if schema == nil {
			return ""
		}
		// Marshal the schema to indented JSON for readability.
		schemaBytes, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			return ""
		}
		return fmt.Sprintf(
			"You must respond with valid JSON that strictly follows this JSON schema:\n```json\n%s\n```\nRespond ONLY with the JSON object, no other text.",
			string(schemaBytes),
		)
	}
	return ""
}

// buildClaudeMessages walks the OpenAI messages and produces Claude-
// shaped messages, merging consecutive same-role messages and flushing
// after any tool_use. tool_result blocks are placed in their own user
// message immediately following the assistant tool_use, as Claude
// requires.
func buildClaudeMessages(src map[string]any) []any {
	rawMsgs, _ := src["messages"].([]any)
	if len(rawMsgs) == 0 {
		return nil
	}

	// Filter out system messages (they go into the system array).
	nonSystem := make([]any, 0, len(rawMsgs))
	for _, mAny := range rawMsgs {
		m, _ := mAny.(map[string]any)
		if m == nil {
			continue
		}
		if role, _ := m["role"].(string); role == "system" {
			continue
		}
		nonSystem = append(nonSystem, mAny)
	}

	var out []any
	var currentRole string
	var currentBlocks []any

	flush := func() {
		if currentRole != "" && len(currentBlocks) > 0 {
			out = append(out, map[string]any{
				"role":    currentRole,
				"content": currentBlocks,
			})
		}
		currentRole = ""
		currentBlocks = nil
	}

	for _, mAny := range nonSystem {
		m, _ := mAny.(map[string]any)
		role, _ := m["role"].(string)
		// "tool" messages in OpenAI become Claude "user" messages with
		// tool_result blocks; "user" stays "user"; everything else
		// becomes "assistant".
		newRole := role
		if role == "tool" || role == "user" {
			newRole = "user"
		} else if role == "assistant" {
			newRole = "assistant"
		} else {
			// Unknown role: pass through as-is.
			newRole = role
		}

		blocks := openaiMessageToBlocks(m)
		hasToolResult := false
		hasToolUse := false
		for _, bAny := range blocks {
			b, _ := bAny.(map[string]any)
			if b == nil {
				continue
			}
			switch b["type"] {
			case "tool_result":
				hasToolResult = true
			case "tool_use":
				hasToolUse = true
			}
		}

		// If message contains tool_result blocks, they must be in a
		// separate user message. Flush any in-progress user message,
		// push the tool_result blocks alone, then continue accumulating
		// the non-tool_result parts under a fresh role.
		if hasToolResult {
			toolResults := []any{}
			others := []any{}
			for _, bAny := range blocks {
				b, _ := bAny.(map[string]any)
				if b != nil && b["type"] == "tool_result" {
					toolResults = append(toolResults, bAny)
				} else {
					others = append(others, bAny)
				}
			}
			flush()
			if len(toolResults) > 0 {
				out = append(out, map[string]any{
					"role":    "user",
					"content": toolResults,
				})
			}
			if len(others) > 0 {
				currentRole = newRole
				currentBlocks = append(currentBlocks, others...)
			}
			continue
		}

		if currentRole != newRole {
			flush()
			currentRole = newRole
		}
		currentBlocks = append(currentBlocks, blocks...)

		// After a tool_use, flush so the next message (which is expected
		// to be the tool result) is in its own message.
		if hasToolUse {
			flush()
		}
	}

	flush()
	return out
}

// openaiMessageToBlocks converts a single OpenAI message into Claude
// content blocks. Pure conversion: no role mapping, no merging.
func openaiMessageToBlocks(m map[string]any) []any {
	switch role := m["role"].(string); role {
	case "tool":
		// tool_call_id + content → tool_result block
		id, _ := m["tool_call_id"].(string)
		return []any{
			map[string]any{
				"type":        "tool_result",
				"tool_use_id": id,
				"content":     contentToString(m["content"]),
			},
		}
	case "user":
		return userContentToBlocks(m)
	case "assistant":
		return assistantContentToBlocks(m)
	default:
		// Unknown role: return a text block with the stringified content.
		return []any{map[string]any{
			"type": "text",
			"text": contentToString(m["content"]),
		}}
	}
}

func userContentToBlocks(m map[string]any) []any {
	switch c := m["content"].(type) {
	case string:
		if c == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": c}}
	case []any:
		var blocks []any
		for _, pAny := range c {
			p, _ := pAny.(map[string]any)
			if p == nil {
				continue
			}
			typ, _ := p["type"].(string)
			switch typ {
			case "text":
				if txt, _ := p["text"].(string); txt != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": txt})
				}
			case "tool_result":
				blk := map[string]any{
					"type":        "tool_result",
					"tool_use_id": p["tool_use_id"],
					"content":     contentToString(p["content"]),
				}
				if isErr, ok := p["is_error"].(bool); ok && isErr {
					blk["is_error"] = true
				}
				blocks = append(blocks, blk)
			case "image_url":
				iu, _ := p["image_url"].(map[string]any)
				url, _ := iu["url"].(string)
				if url == "" {
					continue
				}
				if blk, ok := imageURLToImageBlock(url); ok {
					blocks = append(blocks, blk)
				}
			case "image":
				if src, ok := p["source"].(map[string]any); ok {
					blocks = append(blocks, map[string]any{"type": "image", "source": src})
				}
			}
		}
		return blocks
	}
	return nil
}

func assistantContentToBlocks(m map[string]any) []any {
	var blocks []any
	switch c := m["content"].(type) {
	case string:
		if c != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": c})
		}
	case []any:
		for _, pAny := range c {
			p, _ := pAny.(map[string]any)
			if p == nil {
				continue
			}
			typ, _ := p["type"].(string)
			switch typ {
			case "text":
				if txt, _ := p["text"].(string); txt != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": txt})
				}
			case "tool_use":
				tu := map[string]any{
					"type": "tool_use",
					"id":   p["id"],
					"name": p["name"],
				}
				if input, ok := p["input"]; ok {
					tu["input"] = input
				} else {
					tu["input"] = map[string]any{}
				}
				blocks = append(blocks, tu)
			case "thinking":
				// Strip cache_control if present.
				tb := map[string]any{"type": "thinking"}
				if t, _ := p["thinking"].(string); t != "" {
					tb["thinking"] = t
				}
				if s, _ := p["signature"].(string); s != "" {
					tb["signature"] = s
				}
				blocks = append(blocks, tb)
			}
		}
	}

	// OpenAI tool_calls → Claude tool_use blocks
	if tcs, ok := m["tool_calls"].([]any); ok {
		for _, tcAny := range tcs {
			tc, _ := tcAny.(map[string]any)
			if tc == nil {
				continue
			}
			fn, _ := tc["function"].(map[string]any)
			name, _ := fn["name"].(string)
			id, _ := tc["id"].(string)
			input := parseToolArgs(fn["arguments"])
			blocks = append(blocks, map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  name,
				"input": input,
			})
		}
	}
	return blocks
}

func parseToolArgs(raw any) any {
	switch v := raw.(type) {
	case nil:
		return map[string]any{}
	case string:
		if v == "" {
			return map[string]any{}
		}
		var parsed any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return map[string]any{}
		}
		return parsed
	default:
		return v
	}
}

func imageURLToImageBlock(url string) (map[string]any, bool) {
	const dataPrefix = "data:"
	if !strings.HasPrefix(url, dataPrefix) {
		// External http(s) URL — Claude supports source.url since 2024.
		if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
			return map[string]any{
				"type":   "image",
				"source": map[string]any{"type": "url", "url": url},
			}, true
		}
		return nil, false
	}
	// data:<media>;base64,<data>
	rest := strings.TrimPrefix(url, dataPrefix)
	parts := strings.SplitN(rest, ";", 2)
	if len(parts) != 2 {
		return nil, false
	}
	mediaType := parts[0]
	encAndData := parts[1]
	const base64Prefix = "base64,"
	if !strings.HasPrefix(encAndData, base64Prefix) {
		return nil, false
	}
	data := strings.TrimPrefix(encAndData, base64Prefix)
	if data == "" {
		return nil, false
	}
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": mediaType,
			"data":       data,
		},
	}, true
}

func convertOpenAIToolChoice(raw any) any {
	if raw == nil {
		return map[string]any{"type": "auto"}
	}
	switch v := raw.(type) {
	case string:
		switch v {
		case "required":
			return map[string]any{"type": "any"}
		case "auto", "none":
			return map[string]any{"type": "auto"}
		default:
			return map[string]any{"type": "auto"}
		}
	case map[string]any:
		// Already a Claude-style object?
		if typ, ok := v["type"].(string); ok {
			switch typ {
			case "auto", "any", "tool":
				if typ == "tool" {
					if name, ok := v["name"].(string); ok {
						return map[string]any{"type": "tool", "name": name}
					}
				}
				return map[string]any{"type": typ}
			}
		}
		// OpenAI object shape: {type:"function", function:{name}}
		if fn, ok := v["function"].(map[string]any); ok {
			if name, _ := fn["name"].(string); name != "" {
				return map[string]any{"type": "tool", "name": name}
			}
		}
	}
	return map[string]any{"type": "auto"}
}

func reasoningEffortToBudget(effort string) int {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "xhigh":
		return 32768
	case "high":
		return 16384
	case "medium":
		return 8192
	case "low":
		return 4096
	case "none":
		return 0
	}
	return 0
}
