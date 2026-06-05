package prepost

import (
	"encoding/json"
	"fmt"
)

// AdjustMaxTokens ensures body.max_tokens is large enough for tool use
// and strictly greater than body.thinking.budget_tokens (the Anthropic
// API rejects max_tokens <= budget_tokens).
//
// Rules:
//   - If body.max_tokens is missing or 0 and body.tools is non-empty,
//     set to DefaultMinTokens.
//   - If body.max_tokens is set but < DefaultMinTokens and body.tools is
//     non-empty, bump to DefaultMinTokens.
//   - If body.thinking.budget_tokens is set and body.max_tokens <=
//     budget_tokens, set body.max_tokens = budget_tokens + 1024.
//   - Otherwise leave body.max_tokens alone (and do not invent one if
//     missing and no tools are present).
//
// Operates on an OpenAI-shaped body. Pass-through on parse error.
func AdjustMaxTokens(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("prepost: adjust max tokens: %w", err)
	}

	hasTools := false
	if t, ok := raw["tools"].([]any); ok && len(t) > 0 {
		hasTools = true
	}

	// Existing max_tokens — preserve as float64 since JSON numbers
	// unmarshal to float64 by default.
	var currentMT float64
	hasMT := false
	if v, ok := raw["max_tokens"].(float64); ok {
		currentMT = v
		hasMT = true
	}

	// Existing thinking.budget_tokens.
	var budget float64
	hasBudget := false
	if th, ok := raw["thinking"].(map[string]any); ok {
		if b, ok := th["budget_tokens"].(float64); ok {
			budget = b
			hasBudget = true
		}
	}

	// Compute the new value.
	newMT := currentMT
	if hasMT {
		// bump low max_tokens when tools are present
		if hasTools && newMT < float64(DefaultMinTokens) {
			newMT = float64(DefaultMinTokens)
		}
		// ensure strict-greater than thinking budget
		if hasBudget && newMT <= budget {
			newMT = budget + 1024
		}
	} else {
		// No max_tokens: set one if tools are present.
		if hasTools {
			newMT = float64(DefaultMinTokens)
		}
	}

	if hasMT && newMT == currentMT {
		return body, nil
	}
	if !hasMT && newMT == 0 {
		return body, nil
	}

	raw["max_tokens"] = newMT
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("prepost: marshal: %w", err)
	}
	return out, nil
}
