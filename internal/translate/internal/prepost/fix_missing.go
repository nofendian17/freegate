package prepost

import (
	"encoding/json"
	"fmt"
)

// FixMissingToolResponses scans body.messages and inserts synthetic
// {role:"tool", tool_call_id, content:""} messages after any assistant
// message that has tool_calls but is not followed by a tool response for
// one or more of its tool_call ids.
//
// Operates on OpenAI-shaped bodies (it also tolerates Claude-shaped
// tool_use/tool_result blocks when scanning for "responded" ids).
//
// Pass-through if body has no messages or is malformed JSON at the
// top level (returns the original body).
func FixMissingToolResponses(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("prepost: fix missing tool responses: %w", err)
	}

	msgs, ok := raw["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body, nil
	}

	out := make([]any, 0, len(msgs))
	for i, mAny := range msgs {
		m, _ := mAny.(map[string]any)
		out = append(out, mAny)

		if m == nil {
			continue
		}
		// Only assistant messages with tool_calls need checking.
		if role, _ := m["role"].(string); role != "assistant" {
			continue
		}
		toolCalls, ok := m["tool_calls"].([]any)
		if !ok || len(toolCalls) == 0 {
			continue
		}

		// Collect tool_call ids in this assistant message.
		want := make([]string, 0, len(toolCalls))
		for _, tcAny := range toolCalls {
			tc, _ := tcAny.(map[string]any)
			if id, _ := tc["id"].(string); id != "" {
				want = append(want, id)
			}
		}
		if len(want) == 0 {
			continue
		}

		// Collect ids that are responded in the *next* message.
		responded := map[string]bool{}
		if i+1 < len(msgs) {
			next, _ := msgs[i+1].(map[string]any)
			if next != nil {
				collectRespondedIDs(next, responded)
			}
		}

		// Insert synthetic tool messages for any missing ids.
		for _, id := range want {
			if responded[id] {
				continue
			}
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": id,
				"content":      "",
			})
		}
	}

	raw["messages"] = out
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("prepost: fix missing tool responses: marshal: %w", err)
	}
	return encoded, nil
}

// collectRespondedIDs walks a single message and adds any tool-response
// ids (from OpenAI role:"tool" tool_call_id, or Claude tool_result
// blocks' tool_use_id) into dst.
func collectRespondedIDs(msg map[string]any, dst map[string]bool) {
	if role, _ := msg["role"].(string); role == "tool" {
		if id, _ := msg["tool_call_id"].(string); id != "" {
			dst[id] = true
		}
	}
	if content, ok := msg["content"].([]any); ok {
		for _, partAny := range content {
			part, _ := partAny.(map[string]any)
			if part == nil {
				continue
			}
			if typ, _ := part["type"].(string); typ == "tool_result" {
				if id, _ := part["tool_use_id"].(string); id != "" {
					dst[id] = true
				}
			}
		}
	}
}
