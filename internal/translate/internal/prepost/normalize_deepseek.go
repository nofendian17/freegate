package prepost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// DeepSeekFlashTopP is the default top_p applied to DeepSeek flash requests
// that do not set one. It mirrors opencode's transform.topP default for
// deepseek-v4-flash (packages/opencode/src/provider/transform.ts).
const DeepSeekFlashTopP = 0.95

// IsDeepSeekModel reports whether modelID targets DeepSeek (any variant),
// case-insensitive. It mirrors the api.id contains "deepseek" checks in
// opencode's provider transform.
func IsDeepSeekModel(modelID string) bool {
	return strings.Contains(strings.ToLower(modelID), "deepseek")
}

// IsDeepSeekFlashModel reports whether modelID targets a DeepSeek flash
// variant (deepseek-v4-flash, deepseek-v4-flash-0731, deepseek-v4.1-flash,
// or the deepseek-flash alias). It mirrors opencode's topP gate for
// deepseek-v4-flash plus the flash alias normalization in
// stats/core model-normalization.
func IsDeepSeekFlashModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	return strings.Contains(lower, "deepseek") && strings.Contains(lower, "flash")
}

// NormalizeDeepSeek applies DeepSeek OpenAI-compatible normalization to an
// OpenAI-shaped body:
//
//  1. Every assistant message gets a reasoning_content field. When the
//     message carries "reasoning" but no "reasoning_content", the value is
//     copied; otherwise an empty string is set. DeepSeek requires reasoning
//     state on all assistant turns (opencode's normalizeMessages injects an
//     empty reasoning part for every deepseek assistant message), and its
//     thinking mode requires reasoning_content in conversation history.
//  2. Flash models without an explicit top_p get DeepSeekFlashTopP.
//
// It is a no-op for non-DeepSeek models, empty bodies, and bodies without
// messages. It never overwrites caller-provided reasoning_content or top_p.
func NormalizeDeepSeek(body []byte, modelID string) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	if !IsDeepSeekModel(modelID) {
		return body, nil
	}
	if !bytes.Contains(body, []byte(`"messages"`)) {
		return body, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("prepost: normalize deepseek: %w", err)
	}

	changed := false

	if msgs, ok := raw["messages"].([]any); ok && len(msgs) > 0 {
		for _, mAny := range msgs {
			m, _ := mAny.(map[string]any)
			if m == nil {
				continue
			}
			if role, _ := m["role"].(string); role != "assistant" {
				continue
			}
			if _, hasRC := m["reasoning_content"]; hasRC {
				continue
			}
			if r, hasR := m["reasoning"]; hasR {
				m["reasoning_content"] = r
			} else {
				m["reasoning_content"] = ""
			}
			changed = true
		}
	}

	if IsDeepSeekFlashModel(modelID) {
		if _, ok := raw["top_p"]; !ok {
			raw["top_p"] = DeepSeekFlashTopP
			changed = true
		}
	}

	if !changed {
		return body, nil
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("prepost: normalize deepseek: marshal: %w", err)
	}
	return out, nil
}
