package gemini

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// JSONToOpenAI converts a non-streaming Gemini-format response body to
// OpenAI format. Mirrors 9router's response/gemini-to-openai.js (non-
// streaming path).
//
// Gemini shape:
//
//	{
//	  "candidates": [{
//	    "content": {"parts": [{"text": "..."}, {"functionCall": {...}}], "role": "model"},
//	    "finishReason": "STOP" | "MAX_TOKENS" | "BLOCKED"
//	  }],
//	  "usageMetadata": {"promptTokenCount": N, "candidatesTokenCount": N,
//	                    "thoughtsTokenCount"?: N, "cachedContentTokenCount"?: N,
//	                    "totalTokenCount": N}
//	}
func JSONToOpenAI(body []byte) ([]byte, error) {
	var gemini map[string]any
	if err := json.Unmarshal(body, &gemini); err != nil {
		return nil, fmt.Errorf("gemini: invalid JSONToOpenAI body: %w", err)
	}

	id := "chatcmpl-" + randomID(12)
	created := time.Now().Unix()

	choices := []any{}
	if cands, ok := gemini["candidates"].([]any); ok && len(cands) > 0 {
		c0, _ := cands[0].(map[string]any)
		if c0 != nil {
			choices = append(choices, buildChoiceFromCandidate(c0))
		}
	}

	result := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"choices": choices,
	}

	if um, ok := gemini["usageMetadata"].(map[string]any); ok {
		result["usage"] = buildUsageOpenAI(um)
	}

	out, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("gemini: marshal JSONToOpenAI: %w", err)
	}
	return out, nil
}

func buildChoiceFromCandidate(c map[string]any) map[string]any {
	finishReason := ""
	if fr, ok := c["finishReason"].(string); ok {
		finishReason = mapFinishReasonOpenAI(fr)
	}
	content, _ := c["content"].(map[string]any)
	parts, _ := content["parts"].([]any)

	text, toolCalls := extractContentFromParts(parts)
	msg := map[string]any{"role": "assistant"}
	if toolCalls != nil {
		msg["content"] = text
		msg["tool_calls"] = toolCalls
	} else {
		msg["content"] = text
	}
	return map[string]any{
		"index":         0,
		"message":       msg,
		"finish_reason": finishReason,
	}
}

func extractContentFromParts(parts []any) (string, []any) {
	var sb strings.Builder
	var toolCalls []any
	for _, pAny := range parts {
		p, _ := pAny.(map[string]any)
		if p == nil {
			continue
		}
		if t, ok := p["text"].(string); ok {
			sb.WriteString(t)
		}
		if fc, ok := p["functionCall"].(map[string]any); ok {
			name, _ := fc["name"].(string)
			args, _ := fc["args"].(map[string]any)
			if args == nil {
				args = map[string]any{}
			}
			argsBytes, _ := json.Marshal(args)
			toolCalls = append(toolCalls, map[string]any{
				"id":    "call_gemini_" + name + "_" + randomID(6),
				"type":  "function",
				"index": len(toolCalls),
				"function": map[string]any{
					"name":      name,
					"arguments": string(argsBytes),
				},
			})
		}
	}
	return sb.String(), toolCalls
}

func buildUsageOpenAI(um map[string]any) map[string]any {
	out := map[string]any{}
	var prompt, candidates, thoughts, cached int64
	if v, ok := asInt64(um["promptTokenCount"]); ok {
		prompt = v
	}
	if v, ok := asInt64(um["candidatesTokenCount"]); ok {
		candidates = v
	}
	if v, ok := asInt64(um["thoughtsTokenCount"]); ok {
		thoughts = v
	}
	if v, ok := asInt64(um["cachedContentTokenCount"]); ok {
		cached = v
	}
	completion := candidates + thoughts
	out["prompt_tokens"] = prompt
	out["completion_tokens"] = completion
	out["total_tokens"] = prompt + completion
	if cached > 0 {
		out["prompt_tokens_details"] = map[string]any{
			"cached_tokens": cached,
		}
	}
	if thoughts > 0 {
		out["completion_tokens_details"] = map[string]any{
			"reasoning_tokens": thoughts,
		}
	}
	return out
}

func mapFinishReasonOpenAI(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "BLOCKED", "SAFETY", "RECITATION", "LANGUAGE", "OTHER", "SPII":
		return "content_filter"
	default:
		return "stop"
	}
}

func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

func randomID(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[idx.Int64()]
	}
	return string(b)
}
