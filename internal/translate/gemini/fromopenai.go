package gemini

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FromOpenAI converts an OpenAI-format chat-completions request body to
// Gemini format. Mirrors 9router's request/openai-to-gemini.js (the base
// function openaiToGeminiBase, minus the Gemini-CLI / Cloud-Code
// envelope which is specific to that deployment).
//
// The caller is expected to have already run prepost.* helpers on the
// OpenAI body.
func FromOpenAI(body []byte) ([]byte, error) {
	var src map[string]any
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("gemini: invalid FromOpenAI body: %w", err)
	}

	out := map[string]any{}

	if v, ok := src["model"]; ok {
		out["model"] = v
	}
	if v, ok := src["stream"]; ok {
		out["stream"] = v
	}

	// generationConfig from top-level OpenAI fields
	gc := map[string]any{}
	if v, ok := src["temperature"]; ok {
		gc["temperature"] = v
	}
	if v, ok := src["top_p"]; ok {
		gc["topP"] = v
	}
	if v, ok := src["max_tokens"]; ok {
		gc["maxOutputTokens"] = v
	}
	if v, ok := src["stop"]; ok {
		gc["stopSequences"] = v
	}
	if len(gc) > 0 {
		out["generationConfig"] = gc
	}

	// thinkingConfig from reasoning_effort (OpenAI) or thinking (Claude-native)
	if th, ok := src["thinking"].(map[string]any); ok {
		if budget, ok := th["budget_tokens"]; ok {
			gc["thinkingConfig"] = map[string]any{
				"thinkingBudget":   budget,
				"include_thoughts": true,
			}
		}
	} else if eff, ok := src["reasoning_effort"].(string); ok && eff != "" {
		if budget := reasoningEffortToBudgetGemini(eff); budget > 0 {
			if _, exists := out["generationConfig"]; !exists {
				out["generationConfig"] = map[string]any{}
			}
			out["generationConfig"].(map[string]any)["thinkingConfig"] = map[string]any{
				"thinkingBudget":   budget,
				"include_thoughts": true,
			}
		}
	}

	// systemInstruction: from messages[role=system] (single system msg
	// goes inline; multiple get joined).
	if sys, ok := buildGeminiSystemInstruction(src); ok {
		out["systemInstruction"] = sys
	}

	// messages → contents
	contents, err := buildGeminiContents(src)
	if err != nil {
		return nil, err
	}
	if len(contents) > 0 {
		out["contents"] = contents
	}

	// tools: function → functionDeclarations
	if decls := extractGeminiFunctionDeclarations(src); len(decls) > 0 {
		out["tools"] = []any{map[string]any{"functionDeclarations": decls}}
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("gemini: marshal FromOpenAI: %w", err)
	}
	return encoded, nil
}

func buildGeminiSystemInstruction(src map[string]any) (map[string]any, bool) {
	msgs, ok := src["messages"].([]any)
	if !ok {
		return nil, false
	}
	var parts []any
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
				parts = append(parts, map[string]any{"text": c})
			}
		case []any:
			for _, pAny := range c {
				p, _ := pAny.(map[string]any)
				if p == nil {
					continue
				}
				if typ, _ := p["type"].(string); typ == "text" {
					if txt, _ := p["text"].(string); txt != "" {
						parts = append(parts, map[string]any{"text": txt})
					}
				}
			}
		}
	}
	if len(parts) == 0 {
		return nil, false
	}
	return map[string]any{
		"role":  "user",
		"parts": parts,
	}, true
}

func buildGeminiContents(src map[string]any) ([]any, error) {
	rawMsgs, _ := src["messages"].([]any)
	if len(rawMsgs) == 0 {
		return nil, nil
	}

	// Pre-compute tool_call_id → name map (for functionResponse matching)
	// and tool_call_id → response content map.
	nameByID := map[string]string{}
	respByID := map[string]string{}
	for _, mAny := range rawMsgs {
		m, _ := mAny.(map[string]any)
		if m == nil {
			continue
		}
		role, _ := m["role"].(string)
		if role == "assistant" {
			if tcs, ok := m["tool_calls"].([]any); ok {
				for _, tcAny := range tcs {
					tc, _ := tcAny.(map[string]any)
					if tc == nil {
						continue
					}
					id, _ := tc["id"].(string)
					if id == "" {
						continue
					}
					if fn, _ := tc["function"].(map[string]any); fn != nil {
						if name, _ := fn["name"].(string); name != "" {
							nameByID[id] = name
						}
					}
				}
			}
		} else if role == "tool" {
			id, _ := m["tool_call_id"].(string)
			if id == "" {
				continue
			}
			switch c := m["content"].(type) {
			case string:
				respByID[id] = c
			default:
				b, _ := json.Marshal(c)
				respByID[id] = string(b)
			}
		}
	}

	out := make([]any, 0, len(rawMsgs))
	usedResponses := map[string]bool{}

	for i, mAny := range rawMsgs {
		m, _ := mAny.(map[string]any)
		if m == nil {
			continue
		}
		role, _ := m["role"].(string)
		if role == "system" {
			// Already extracted into systemInstruction.
			continue
		}

		switch role {
		case "user":
			parts := openaiUserContentToParts(m)
			if len(parts) == 0 {
				continue
			}
			out = append(out, map[string]any{"role": "user", "parts": parts})

		case "tool":
			id, _ := m["tool_call_id"].(string)
			if id == "" {
				continue
			}
			if usedResponses[id] {
				// Already emitted by the assistant look-ahead.
				continue
			}
			name := nameByID[id]
			if name == "" {
				name = id
			}
			content, _ := m["content"].(string)
			if content == "" {
				// Try to JSON-encode non-string content
				b, _ := json.Marshal(m["content"])
				content = string(b)
			}
			part := map[string]any{
				"functionResponse": map[string]any{
					"id":   id,
					"name": name,
					"response": map[string]any{
						"result": tryParseJSON(content),
					},
				},
			}
			out = append(out, map[string]any{"role": "user", "parts": []any{part}})
			usedResponses[id] = true

		case "assistant":
			parts, tcIDs := openaiAssistantContentToParts(m)
			out = append(out, map[string]any{"role": "model", "parts": parts})
			// If the model emitted functionCall parts and the
			// corresponding functionResponse parts exist (from
			// following "tool" messages in the OpenAI body), emit them
			// as a follow-up user content so the conversation order
			// is preserved for Gemini.
			if len(tcIDs) > 0 {
				respParts := []any{}
				for _, id := range tcIDs {
					if usedResponses[id] {
						continue
					}
					content, ok := respByID[id]
					if !ok {
						continue
					}
					usedResponses[id] = true
					name := nameByID[id]
					if name == "" {
						name = id
					}
					respParts = append(respParts, map[string]any{
						"functionResponse": map[string]any{
							"id":   id,
							"name": name,
							"response": map[string]any{
								"result": tryParseJSON(content),
							},
						},
					})
				}
				if len(respParts) > 0 {
					out = append(out, map[string]any{"role": "user", "parts": respParts})
				}
			}
		}

		_ = i
	}

	// After processing: if any tool response was not yet emitted (the
	// "tool" message appeared *before* the "assistant" tool_calls in
	// the OpenAI body — unusual but possible), emit a follow-up user
	// content for the remaining ids.
	if len(usedResponses) < len(respByID) {
		var remaining []any
		for id, content := range respByID {
			if usedResponses[id] {
				continue
			}
			name := nameByID[id]
			if name == "" {
				name = id
			}
			remaining = append(remaining, map[string]any{
				"functionResponse": map[string]any{
					"id":   id,
					"name": name,
					"response": map[string]any{
						"result": tryParseJSON(content),
					},
				},
			})
		}
		if len(remaining) > 0 {
			out = append(out, map[string]any{"role": "user", "parts": remaining})
		}
	}

	return out, nil
}

func openaiUserContentToParts(m map[string]any) []any {
	var parts []any
	switch c := m["content"].(type) {
	case string:
		if c != "" {
			parts = append(parts, map[string]any{"text": c})
		}
		return parts
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
					parts = append(parts, map[string]any{"text": txt})
				}
			case "image_url":
				iu, _ := p["image_url"].(map[string]any)
				url, _ := iu["url"].(string)
				if url == "" {
					continue
				}
				if blk, ok := imageURLToInlineData(url); ok {
					parts = append(parts, blk)
				}
			case "tool_result":
				id, _ := p["tool_use_id"].(string)
				if id == "" {
					continue
				}
				content := contentToStringForGemini(p["content"])
				parts = append(parts, map[string]any{
					"functionResponse": map[string]any{
						"id":   id,
						"name": id,
						"response": map[string]any{
							"result": tryParseJSON(content),
						},
					},
				})
			}
		}
	}
	return parts
}

func openaiAssistantContentToParts(m map[string]any) ([]any, []string) {
	var parts []any
	var toolCallIDs []string

	// text content
	switch c := m["content"].(type) {
	case string:
		if c != "" {
			parts = append(parts, map[string]any{"text": c})
		}
	case []any:
		for _, pAny := range c {
			p, _ := pAny.(map[string]any)
			if p == nil {
				continue
			}
			if typ, _ := p["type"].(string); typ == "text" {
				if txt, _ := p["text"].(string); txt != "" {
					parts = append(parts, map[string]any{"text": txt})
				}
			}
		}
	}

	// tool_calls → functionCall
	if tcs, ok := m["tool_calls"].([]any); ok {
		for _, tcAny := range tcs {
			tc, _ := tcAny.(map[string]any)
			if tc == nil {
				continue
			}
			id, _ := tc["id"].(string)
			fn, _ := tc["function"].(map[string]any)
			name, _ := fn["name"].(string)
			if name == "" {
				continue
			}
			args := tryParseJSON(contentToStringForGemini(fn["arguments"]))
			parts = append(parts, map[string]any{
				"functionCall": map[string]any{
					"id":   id,
					"name": name,
					"args": args,
				},
			})
			toolCallIDs = append(toolCallIDs, id)
		}
	}
	return parts, toolCallIDs
}

func extractGeminiFunctionDeclarations(src map[string]any) []any {
	tools, ok := src["tools"].([]any)
	if !ok {
		return nil
	}
	var decls []any
	for _, tAny := range tools {
		t, _ := tAny.(map[string]any)
		if t == nil {
			continue
		}
		var name, description string
		var params any

		// OpenAI shape
		if ttype, _ := t["type"].(string); ttype == "function" || ttype == "" {
			if fn, _ := t["function"].(map[string]any); fn != nil {
				name, _ = fn["name"].(string)
				description, _ = fn["description"].(string)
				params = fn["parameters"]
			}
		}
		// Claude shape passthrough
		if name == "" {
			if n, _ := t["name"].(string); n != "" {
				name = n
				description, _ = t["description"].(string)
				params = t["input_schema"]
			}
		}
		if name == "" {
			continue
		}
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		decls = append(decls, map[string]any{
			"name":        name,
			"description": description,
			"parameters":  params,
		})
	}
	return decls
}

func imageURLToInlineData(url string) (map[string]any, bool) {
	const dataPrefix = "data:"
	if !strings.HasPrefix(url, dataPrefix) {
		// Gemini only accepts base64 inline data. http(s) URLs would
		// need to be fetched by the upstream; pass through as text hint.
		return nil, false
	}
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
		"inlineData": map[string]any{
			"mimeType": mediaType,
			"data":     data,
		},
	}, true
}

func contentToStringForGemini(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func tryParseJSON(s string) any {
	if s == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}

func reasoningEffortToBudgetGemini(effort string) int {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low":
		return 1024
	case "medium":
		return 8192
	case "high":
		return 32768
	}
	return 0
}
