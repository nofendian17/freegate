package prepost

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// PrepareUpstream applies the three handler-level normalizations that were
// previously done as three separate JSON passes (NormalizeRoles,
// NormalizeRequestReasoning, EnsureStreamOptions) in a single pass.
//
// It performs one Unmarshal, applies all mutations on the same map, and
// marshals once. This reduces allocs from 3x to 1x on the hot path.
//
// For model-aware DeepSeek normalization (reasoning_content guarantee,
// flash top_p default), use PrepareUpstreamWithModel.
func PrepareUpstream(body []byte) ([]byte, []string, error) {
	return PrepareUpstreamWithModel(body, "")
}

// PrepareUpstreamWithModel is PrepareUpstream plus DeepSeek model-aware
// normalization for OpenAI-targeted bodies: every assistant message gets a
// reasoning_content field (copied from "reasoning" when present, "" when
// absent), and flash models without top_p get DeepSeekFlashTopP. Non-DeepSeek
// models behave exactly like PrepareUpstream.
//
// The returned tokens name each normalization that fired (see the Applied*
// constants) for the X-Fg-Normalized response header.
func PrepareUpstreamWithModel(body []byte, modelID string) ([]byte, []string, error) {
	if len(body) == 0 {
		return body, nil, nil
	}
	isDeepSeek := IsDeepSeekModel(modelID)
	isFlash := IsDeepSeekFlashModel(modelID)
	// Fast path: if none of the relevant keys are present, avoid parsing.
	// A DeepSeek model may still need reasoning_content injected into
	// messages that carry no reasoning key at all, so require "messages"
	// before skipping the parse.
	hasDeveloper := bytes.Contains(body, []byte(`"developer"`))
	hasReasoning := bytes.Contains(body, []byte(`"reasoning":`))
	hasStream := bytes.Contains(body, []byte(`"stream"`))
	hasTopP := bytes.Contains(body, []byte(`"top_p"`))
	hasMessages := bytes.Contains(body, []byte(`"messages"`))
	hasTools := bytes.Contains(body, []byte(`"tools"`))
	needsParse := hasDeveloper || hasReasoning || hasStream || (isFlash && !hasTopP && hasMessages) ||
		(isDeepSeek && (hasMessages || hasTools))
	if !needsParse {
		return body, nil, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, fmt.Errorf("prepost: prepare upstream: %w", err)
	}

	var applied []string
	mark := func(token string) {
		applied = append(applied, token)
	}

	// 1. Normalize roles: developer -> system
	if hasDeveloper {
		if msgs, ok := raw["messages"].([]any); ok && len(msgs) > 0 {
			roleChanged := false
			for i, mAny := range msgs {
				m, _ := mAny.(map[string]any)
				if m == nil {
					continue
				}
				if role, _ := m["role"].(string); role == "developer" {
					m["role"] = "system"
					msgs[i] = m
					roleChanged = true
				}
			}
			if roleChanged {
				raw["messages"] = msgs
				mark(AppliedDeveloperSystem)
			}
		}
	}

	// 2. Normalize request reasoning: reasoning -> reasoning_content for assistant.
	// For DeepSeek models, also guarantee reasoning_content on every
	// assistant message ("" when neither field is present).
	if hasReasoning || (isDeepSeek && hasMessages) {
		if msgs, ok := raw["messages"].([]any); ok && len(msgs) > 0 {
			reasonChanged := false
			for _, mAny := range msgs {
				m, _ := mAny.(map[string]any)
				if m == nil {
					continue
				}
				if role, _ := m["role"].(string); role != "assistant" {
					continue
				}
				_, hasRC := m["reasoning_content"]
				r, hasR := m["reasoning"]
				if hasR && !hasRC {
					m["reasoning_content"] = r
					reasonChanged = true
				} else if isDeepSeek && !hasRC && !hasR {
					m["reasoning_content"] = ""
					reasonChanged = true
				}
			}
			if reasonChanged {
				mark(AppliedReasoningContent)
			}
		}
	}

	// 3. Ensure stream_options when stream == true
	if hasStream {
		if stream, _ := raw["stream"].(bool); stream {
			if _, ok := raw["stream_options"]; !ok {
				raw["stream_options"] = map[string]any{"include_usage": true}
				mark(AppliedStreamOptions)
			}
		}
	}

	// 4. Flash top_p default for DeepSeek flash models.
	if isFlash {
		if _, ok := raw["top_p"]; !ok {
			raw["top_p"] = DeepSeekFlashTopP
			mark(AppliedDeepSeekFlashTopP)
		}
	}

	// 5. DSML tool-call stop for DeepSeek tool requests (vllm#54686 port:
	// the closer ends the turn, so generation stops there instead of
	// re-emitting blocks to max_tokens). Tools absent/empty means no tool
	// calls can occur, so stop is left alone.
	if isDeepSeek {
		if tools, ok := raw["tools"].([]any); ok && len(tools) > 0 {
			if ensureDSMLToolStop(raw) {
				mark(AppliedDeepSeekToolStop)
			}
		}
	}

	if len(applied) == 0 {
		return body, nil, nil
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("prepost: prepare upstream: marshal: %w", err)
	}
	return out, applied, nil
}
