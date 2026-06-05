package prepost

import (
	"encoding/json"
	"fmt"
)

// SanitizeToolHistory cleans up orphaned tool interactions at the edges
// of a conversation history. This is necessary when upstream providers
// or clients truncate conversation history at message boundaries that
// split tool_use / tool_result pairs.
//
// Steps:
//  1. Strip leading orphaned tool messages — role:"tool" messages at the
//     start of the conversation that have no preceding assistant
//     tool_calls to pair with.
//  2. Strip trailing orphaned tool_calls — if the last assistant message
//     has tool_calls but no following tool responses, remove the
//     tool_calls field (keeping any text content in that message).
//
// This runs BEFORE FixMissingToolResponses, so that function only
// handles mid-conversation gaps rather than edge artifacts.
func SanitizeToolHistory(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("prepost: sanitize tool history: %w", err)
	}

	msgs, ok := raw["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body, nil
	}

	changed := false

	// Step 1: Strip leading orphaned tool messages.
	// These are role:"tool" messages at the start that reference
	// tool_call_ids from a truncated-away assistant message.
	startIdx := 0
	for startIdx < len(msgs) {
		m, _ := msgs[startIdx].(map[string]any)
		if m == nil {
			break
		}
		role, _ := m["role"].(string)
		if role != "tool" {
			break
		}
		startIdx++
		changed = true
	}
	// Also strip leading user messages whose ONLY content is
	// tool_result blocks (Claude-shaped orphans).
	for startIdx < len(msgs) {
		m, _ := msgs[startIdx].(map[string]any)
		if m == nil {
			break
		}
		role, _ := m["role"].(string)
		if role != "user" {
			break
		}
		if !isOnlyToolResults(m) {
			break
		}
		startIdx++
		changed = true
	}

	if startIdx > 0 {
		msgs = msgs[startIdx:]
	}

	// Step 2: Strip trailing orphaned tool_calls.
	// Walk backwards from the end to find the last assistant message.
	// If it has tool_calls but no following tool responses, remove the
	// tool_calls field.
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m == nil {
			continue
		}
		role, _ := m["role"].(string)
		if role != "assistant" {
			continue
		}

		tcs, hasTCs := m["tool_calls"].([]any)
		if !hasTCs || len(tcs) == 0 {
			break // last assistant has no tool_calls, nothing to do
		}

		// Check if any subsequent message responds to these tool_calls.
		hasResponse := false
		responded := map[string]bool{}
		for j := i + 1; j < len(msgs); j++ {
			next, _ := msgs[j].(map[string]any)
			if next == nil {
				continue
			}
			collectRespondedIDs(next, responded)
		}
		if len(responded) > 0 {
			hasResponse = true
		}

		if !hasResponse {
			// Remove tool_calls from this assistant message.
			delete(m, "tool_calls")
			changed = true

			// If the message now has no content at all, set content
			// to a placeholder so it's not an empty assistant message.
			if !hasValidContent(m) {
				m["content"] = "[Previous tool invocations were truncated from context]"
			}
		}
		break // only check the last assistant message
	}

	if !changed {
		return body, nil
	}

	raw["messages"] = msgs
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("prepost: sanitize tool history: marshal: %w", err)
	}
	return encoded, nil
}

// isOnlyToolResults reports whether a message's content consists
// entirely of tool_result blocks (Claude-shaped orphans).
func isOnlyToolResults(m map[string]any) bool {
	content, ok := m["content"].([]any)
	if !ok || len(content) == 0 {
		return false
	}
	for _, pAny := range content {
		p, _ := pAny.(map[string]any)
		if p == nil {
			return false
		}
		typ, _ := p["type"].(string)
		if typ != "tool_result" {
			return false
		}
	}
	return true
}
