package prepost

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// NormalizeClaudeContent strips content blocks the Anthropic API rejects
// with a 400 invalid_request_error:
//
//  1. text blocks with empty text ({"type":"text","text":""}),
//  2. thinking blocks with empty thinking and no signature (unsigned
//     replays the API cannot verify — mirrors opencode's filter that drops
//     reasoning parts lacking signature/redactedData in
//     packages/opencode/src/provider/transform.ts),
//  3. redacted_thinking blocks with empty data.
//
// Messages left without content by the stripping are dropped, except a
// final assistant message (same rule as dropEmptyMessages). Non-empty
// blocks — including signed thinking blocks replayed for multi-turn
// reasoning — are preserved verbatim. Idempotent: running it twice changes
// nothing the second time.
//
// The returned tokens name each normalization that fired (see the Applied*
// constants) for the X-Fg-Normalized response header.
func NormalizeClaudeContent(body []byte) ([]byte, []string, error) {
	if len(body) == 0 {
		return body, nil, nil
	}
	// Fast path: without these keys there is nothing to strip. Tools are
	// checked separately: schemas with Unicode property escapes are
	// dropped below even when no message content needs stripping.
	hasContent := bytes.Contains(body, []byte(`"content"`))
	hasBadPattern := bytes.Contains(body, []byte(`"tools"`)) && HasUnicodePropertyPattern(body)
	if !hasContent && !hasBadPattern {
		return body, nil, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, fmt.Errorf("prepost: normalize claude content: %w", err)
	}

	var applied []string
	mark := func(token string) {
		for _, t := range applied {
			if t == token {
				return
			}
		}
		applied = append(applied, token)
	}

	// Drop Unicode-property regex patterns from tool schemas up front:
	// strict providers reject \p{...} with invalid_request_error even on
	// Claude-native endpoints.
	if hasBadPattern {
		if tools, ok := raw["tools"].([]any); ok {
			dropped := 0
			for _, t := range tools {
				dropped += SanitizeUnsupportedPatterns(t)
			}
			if dropped > 0 {
				mark(AppliedToolPatternDrop)
			}
		}
	}

	msgs, ok := raw["messages"].([]any)
	if !ok || len(msgs) == 0 {
		if len(applied) == 0 {
			return body, nil, nil
		}
		out, err := json.Marshal(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("prepost: normalize claude content: marshal: %w", err)
		}
		return out, applied, nil
	}

	if stripRejectableBlocks(msgs) {
		mark(AppliedClaudeStripEmpty)
	}

	filtered := dropEmptyMessages(msgs)
	if len(filtered) != len(msgs) {
		mark(AppliedClaudeStripEmpty)
	}
	raw["messages"] = filtered

	if len(applied) == 0 {
		return body, nil, nil
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("prepost: normalize claude content: marshal: %w", err)
	}
	return out, applied, nil
}

// stripRejectableBlocks removes Anthropic-rejected blocks from each
// message's content array in place and reports whether anything was
// removed.
func stripRejectableBlocks(msgs []any) bool {
	changed := false
	for _, mAny := range msgs {
		m, _ := mAny.(map[string]any)
		if m == nil {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok || len(content) == 0 {
			continue
		}
		kept := content[:0]
		for _, pAny := range content {
			p, _ := pAny.(map[string]any)
			if p == nil {
				continue
			}
			if isRejectableBlock(p) {
				changed = true
				continue
			}
			kept = append(kept, pAny)
		}
		// Zero the tail so dropped blocks are not retained by the array.
		for i := len(kept); i < len(content); i++ {
			content[i] = nil
		}
		m["content"] = kept
	}
	return changed
}

// isRejectableBlock reports whether a Claude content block is rejected by
// the Anthropic API: empty text, unsigned empty thinking, or empty
// redacted_thinking.
func isRejectableBlock(p map[string]any) bool {
	typ, _ := p["type"].(string)
	switch typ {
	case "text":
		text, _ := p["text"].(string)
		return text == ""
	case "thinking":
		thinking, _ := p["thinking"].(string)
		signature, _ := p["signature"].(string)
		return thinking == "" && signature == ""
	case "redacted_thinking":
		data, _ := p["data"].(string)
		return data == ""
	}
	return false
}
