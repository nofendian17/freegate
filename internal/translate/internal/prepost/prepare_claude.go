package prepost

import (
	"encoding/json"
	"fmt"
	"maps"
)

// PrepareClaudeRequest normalizes a Claude-target body for the Anthropic
// API. Steps:
//
//  1. System array: strip all cache_control, then add
//     cache_control:{type:"ephemeral", ttl:"1h"} only to the last block.
//  2. Messages: drop empty messages; keep the final assistant even if
//     empty.
//  3. Tool_use ordering: in each assistant message, drop text blocks
//     that come AFTER a tool_use block. The Claude API rejects text
//     after tool_use within the same content array.
//  4. Merge consecutive same-role messages.
//  5. Tools: strip cache_control, re-add only to the last tool.
//
// Steps 1-2 are applied first, then 3-4 (ordering and merging) on the
// filtered message list, then 5 on the tool list.
func PrepareClaudeRequest(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("prepost: prepare claude: %w", err)
	}

	// 1. System normalization
	normalizeSystemCacheControl(raw)

	// 2. Drop empty messages (keep final assistant)
	if msgs, ok := raw["messages"].([]any); ok && len(msgs) > 0 {
		filtered := dropEmptyMessages(msgs)
		// 3. Fix tool_use ordering
		fixToolUseOrderingInPlace(filtered)
		// 4. Merge consecutive same-role messages
		merged := mergeConsecutiveSameRole(filtered)
		raw["messages"] = merged
	}

	// 5. Tools cache_control
	normalizeToolsCacheControl(raw)

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("prepost: prepare claude: marshal: %w", err)
	}
	return out, nil
}

// normalizeSystemCacheControl strips all cache_control from blocks in
// the system array, then re-adds it to the last block only. No-op if
// system is a string or absent.
func normalizeSystemCacheControl(raw map[string]any) {
	sys, ok := raw["system"].([]any)
	if !ok || len(sys) == 0 {
		return
	}
	for _, bAny := range sys {
		b, _ := bAny.(map[string]any)
		if b == nil {
			continue
		}
		delete(b, "cache_control")
	}
	// Re-add cache_control only to the last block.
	last, _ := sys[len(sys)-1].(map[string]any)
	if last == nil {
		return
	}
	last["cache_control"] = map[string]any{
		"type": "ephemeral",
		"ttl":  "1h",
	}
}

// dropEmptyMessages returns a new slice with empty messages removed,
// except the last message if it is an assistant message.
func dropEmptyMessages(msgs []any) []any {
	out := make([]any, 0, len(msgs))
	for i, mAny := range msgs {
		m, _ := mAny.(map[string]any)
		isLast := i == len(msgs)-1
		keep := false
		if m != nil {
			role, _ := m["role"].(string)
			if isLast && role == "assistant" {
				// Always keep the final assistant
				keep = true
			} else {
				keep = hasValidContent(m)
			}
		}
		if keep {
			out = append(out, mAny)
		}
	}
	return out
}

// hasValidContent reports whether m carries any meaningful payload:
// a non-empty string, a non-empty array, or tool_calls (OpenAI shape).
func hasValidContent(m map[string]any) bool {
	// OpenAI tool_calls count as content even if content is "".
	if tcs, ok := m["tool_calls"].([]any); ok && len(tcs) > 0 {
		return true
	}
	switch c := m["content"].(type) {
	case string:
		return c != ""
	case []any:
		if len(c) == 0 {
			return false
		}
		for _, pAny := range c {
			p, _ := pAny.(map[string]any)
			if p == nil {
				continue
			}
			typ, _ := p["type"].(string)
			switch typ {
			case "text":
				if txt, _ := p["text"].(string); txt != "" {
					return true
				}
			case "tool_use", "tool_result", "image":
				return true
			}
		}
		return false
	}
	return false
}

// fixToolUseOrderingInPlace rewrites each assistant message that has
// both text and tool_use blocks: it drops text blocks that appear AFTER
// the first tool_use. Thinking blocks are preserved.
func fixToolUseOrderingInPlace(msgs []any) {
	for _, mAny := range msgs {
		m, _ := mAny.(map[string]any)
		if m == nil {
			continue
		}
		role, _ := m["role"].(string)
		if role != "assistant" {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		hasToolUse := false
		for _, pAny := range content {
			p, _ := pAny.(map[string]any)
			if p != nil {
				if typ, _ := p["type"].(string); typ == "tool_use" {
					hasToolUse = true
					break
				}
			}
		}
		if !hasToolUse {
			continue
		}
		filtered := make([]any, 0, len(content))
		foundToolUse := false
		for _, pAny := range content {
			p, _ := pAny.(map[string]any)
			typ := ""
			if p != nil {
				typ, _ = p["type"].(string)
			}
			switch {
			case typ == "tool_use":
				foundToolUse = true
				filtered = append(filtered, pAny)
			case typ == "thinking" || typ == "redacted_thinking":
				filtered = append(filtered, pAny)
			case !foundToolUse:
				filtered = append(filtered, pAny)
				// text blocks before tool_use are kept
			default:
				// text blocks after tool_use are dropped
			}
		}
		m["content"] = filtered
	}
}

// mergeConsecutiveSameRole walks the message list and merges any
// adjacent messages with the same role by concatenating their content
// arrays. Tool_result blocks are placed first in the merged content.
func mergeConsecutiveSameRole(msgs []any) []any {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]any, 0, len(msgs))
	for _, mAny := range msgs {
		m, _ := mAny.(map[string]any)
		if m == nil {
			out = append(out, mAny)
			continue
		}
		role, _ := m["role"].(string)
		lastIdx := len(out) - 1
		if lastIdx >= 0 {
			prev, _ := out[lastIdx].(map[string]any)
			if prev != nil {
				if prevRole, _ := prev["role"].(string); prevRole == role {
					mergeInto(prev, m)
					continue
				}
			}
		}
		// Ensure content is an array on the cloned message.
		cloned := cloneMap(m)
		if _, ok := cloned["content"].([]any); !ok {
			if s, ok := cloned["content"].(string); ok {
				if s == "" {
					cloned["content"] = []any{}
				} else {
					cloned["content"] = []any{map[string]any{"type": "text", "text": s}}
				}
			} else {
				cloned["content"] = []any{}
			}
		}
		out = append(out, cloned)
	}
	return out
}

// mergeInto appends the source message's content into dst's content
// array. tool_result blocks from both are placed first.
func mergeInto(dst, src map[string]any) {
	dstContent, _ := dst["content"].([]any)
	srcContent, _ := src["content"].([]any)
	if dstContent == nil {
		dstContent = []any{}
	}
	if srcContent == nil {
		srcContent = []any{}
	}
	toolResults := []any{}
	other := []any{}
	for _, p := range dstContent {
		if isToolResult(p) {
			toolResults = append(toolResults, p)
		} else {
			other = append(other, p)
		}
	}
	for _, p := range srcContent {
		if isToolResult(p) {
			toolResults = append(toolResults, p)
		} else {
			other = append(other, p)
		}
	}
	merged := append(toolResults, other...)
	dst["content"] = merged
}

func isToolResult(p any) bool {
	m, _ := p.(map[string]any)
	if m == nil {
		return false
	}
	typ, _ := m["type"].(string)
	return typ == "tool_result"
}

// normalizeToolsCacheControl strips all cache_control from each tool
// and re-adds it to the last tool only.
func normalizeToolsCacheControl(raw map[string]any) {
	tools, ok := raw["tools"].([]any)
	if !ok || len(tools) == 0 {
		return
	}
	for _, tAny := range tools {
		t, _ := tAny.(map[string]any)
		if t != nil {
			delete(t, "cache_control")
		}
	}
	last, _ := tools[len(tools)-1].(map[string]any)
	if last == nil {
		return
	}
	last["cache_control"] = map[string]any{
		"type": "ephemeral",
		"ttl":  "1h",
	}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	maps.Copy(out, m)
	return out
}
