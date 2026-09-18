package upstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"freegate/internal/translate/claude"
)

// SSE-to-JSON assembly for the OpenCode Zen gateway.
//
// Anonymous free-tier requests are only served as streams (non-streaming
// bodies get 403 FreeTierError), so non-streaming client requests are
// upgraded to stream:true upstream and reassembled here into a single
// native-format JSON object. Downstream (normalization + client-format
// translation) then proceeds exactly as for native non-streaming bodies.

// assembleUpstreamStream reads one SSE event stream and folds it into a
// single native-format JSON response: chat.completion for /chat/completions,
// the completed response object for /responses, Claude message for
// /messages. model is a fallback when chunks carry none. maxBytes bounds
// the read; truncation or an unrecognized stream is an error.
func assembleUpstreamStream(endpoint string, r io.Reader, model string, maxBytes int64) ([]byte, error) {
	events, err := readSSEEvents(r, maxBytes)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.HasSuffix(endpoint, "/responses"):
		return assembleResponsesObject(events)
	case strings.HasSuffix(endpoint, "/messages"):
		return assembleClaudeMessage(events, model)
	default:
		return assembleChatCompletion(events, model)
	}
}

// readSSEEvents collects data payloads in order, joining multi-line data
// fields per the SSE spec. Stops at the [DONE] terminator.
func readSSEEvents(r io.Reader, maxBytes int64) ([]string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("assemble: read stream: %w", err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("assemble: stream exceeds %d bytes", maxBytes)
	}
	var events []string
	var data []string
	flush := func() {
		if len(data) > 0 {
			events = append(events, strings.Join(data, "\n"))
			data = nil
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				flush()
				break
			}
			data = append(data, payload)
		}
		// event: and other field lines only delimit; payload comes from data:.
		if !strings.HasPrefix(line, "event:") && !strings.HasPrefix(line, "data:") {
			flush()
		}
	}
	flush()
	return events, nil
}

func decodeChunk(raw string) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, false
	}
	return m, true
}

func strVal(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func intVal(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

type toolCallAcc struct {
	index     int
	id        string
	typ       string
	name      string
	args      strings.Builder
	seenIndex bool
}

// assembleChatCompletion folds OpenAI chat.completion.chunk deltas into one
// chat.completion object. Content fragments concatenate; tool_call
// arguments accumulate per index and stay valid JSON; the last finish
// reason and usage win.
func assembleChatCompletion(events []string, model string) ([]byte, error) {
	var (
		id, created any
		content     strings.Builder
		hasContent  bool
		reasoning   strings.Builder
		calls       = map[int]*toolCallAcc{}
		order       []int
		finish      string
		usage       map[string]any
	)
	sawPayload := false
	for _, raw := range events {
		chunk, ok := decodeChunk(raw)
		if !ok {
			continue
		}
		sawPayload = true
		if id == nil {
			id = chunk["id"]
		}
		if created == nil {
			created = chunk["created"]
		}
		if m, _ := chunk["model"].(string); m != "" {
			model = m
		}
		if u, ok := chunk["usage"].(map[string]any); ok {
			usage = u
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		if fr := strVal(choice, "finish_reason"); fr != "" && fr != "null" {
			finish = fr
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		if text, ok := delta["content"].(string); ok && text != "" {
			content.WriteString(text)
			hasContent = true
		}
		// Some providers (e.g. mimo) stream thinking as a sibling
		// "reasoning" delta; keep it so downstream reasoning sync sees it.
		if text, ok := delta["reasoning"].(string); ok && text != "" {
			reasoning.WriteString(text)
		}
		tcs, _ := delta["tool_calls"].([]any)
		for _, tcAny := range tcs {
			tc, _ := tcAny.(map[string]any)
			if tc == nil {
				continue
			}
			idx := int(intVal(tc["index"]))
			if _, ok := tc["index"]; !ok {
				idx = len(order)
			}
			acc, ok := calls[idx]
			if !ok {
				acc = &toolCallAcc{index: idx}
				calls[idx] = acc
				order = append(order, idx)
			}
			if v := strVal(tc, "id"); v != "" {
				acc.id = v
			}
			if v := strVal(tc, "type"); v != "" {
				acc.typ = v
			}
			if fn, ok := tc["function"].(map[string]any); ok {
				if v := strVal(fn, "name"); v != "" {
					acc.name = v
				}
				if v, ok := fn["arguments"].(string); ok {
					acc.args.WriteString(v)
				}
			}
		}
	}
	if !sawPayload {
		return nil, fmt.Errorf("assemble: no chat completion chunks")
	}
	if id == nil || id == "" {
		id = "chatcmpl-assembled"
	}
	if created == nil {
		created = time.Now().Unix()
	}
	var toolCalls []any
	for _, idx := range order {
		acc := calls[idx]
		fn := map[string]any{"arguments": acc.args.String()}
		if acc.name != "" {
			fn["name"] = acc.name
		}
		tc := map[string]any{"index": acc.index, "function": fn}
		if acc.id != "" {
			tc["id"] = acc.id
		}
		if acc.typ != "" {
			tc["type"] = acc.typ
		}
		toolCalls = append(toolCalls, tc)
	}
	var contentVal any = content.String()
	if !hasContent && len(toolCalls) > 0 {
		contentVal = nil
	}
	message := map[string]any{"role": "assistant", "content": contentVal}
	if reasoning.Len() > 0 {
		message["reasoning"] = reasoning.String()
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	out := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finish,
		}},
	}
	if usage != nil {
		out["usage"] = usage
	}
	return json.Marshal(out)
}

// assembleResponsesObject extracts the terminal response object from a
// Responses API event stream. response.completed carries it whole;
// response.incomplete (e.g. max_output_tokens exhausted by reasoning)
// carries the partial response with status "incomplete" — still a valid,
// faithful result. Completed is preferred when both appear.
func assembleResponsesObject(events []string) ([]byte, error) {
	var fallback []byte
	for _, raw := range events {
		evt, ok := decodeChunk(raw)
		if !ok {
			continue
		}
		typ := strVal(evt, "type")
		if typ != "response.completed" && typ != "response.incomplete" {
			continue
		}
		resp, ok := evt["response"].(map[string]any)
		if !ok {
			continue
		}
		out, err := json.Marshal(resp)
		if err != nil {
			continue
		}
		if typ == "response.completed" {
			return out, nil
		}
		fallback = out
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, fmt.Errorf("assemble: no response.completed event")
}

type claudeBlockAcc struct {
	typ    string
	id     string
	name   string
	text   strings.Builder
	args   strings.Builder
	isTool bool
}

// assembleClaudeMessage folds Anthropic Messages SSE events into one
// message object. Text and tool-use input deltas accumulate per block;
// tool input is repaired into a JSON object.
func assembleClaudeMessage(events []string, model string) ([]byte, error) {
	var (
		id         string
		usage      = map[string]any{}
		blocks     = map[int]*claudeBlockAcc{}
		order      []int
		stopReason string
		sawPayload bool
	)
	mergeUsage := func(u map[string]any) {
		for k, v := range u {
			usage[k] = v
		}
	}
	for _, raw := range events {
		evt, ok := decodeChunk(raw)
		if !ok {
			continue
		}
		sawPayload = true
		switch strVal(evt, "type") {
		case "message_start":
			if msg, ok := evt["message"].(map[string]any); ok {
				if v := strVal(msg, "id"); v != "" {
					id = v
				}
				if v := strVal(msg, "model"); v != "" {
					model = v
				}
				if u, ok := msg["usage"].(map[string]any); ok {
					mergeUsage(u)
				}
			}
		case "content_block_start":
			idx := int(intVal(evt["index"]))
			block, _ := evt["content_block"].(map[string]any)
			acc := &claudeBlockAcc{typ: strVal(block, "type")}
			if acc.typ == "tool_use" {
				acc.isTool = true
				acc.id = strVal(block, "id")
				acc.name = strVal(block, "name")
			}
			blocks[idx] = acc
			order = append(order, idx)
		case "content_block_delta":
			idx := int(intVal(evt["index"]))
			acc, ok := blocks[idx]
			if !ok {
				continue
			}
			delta, _ := evt["delta"].(map[string]any)
			switch strVal(delta, "type") {
			case "text_delta":
				if t, ok := delta["text"].(string); ok {
					acc.text.WriteString(t)
				}
			case "input_json_delta":
				if p, ok := delta["partial_json"].(string); ok {
					acc.args.WriteString(p)
				}
			}
		case "message_delta":
			if delta, ok := evt["delta"].(map[string]any); ok {
				if sr := strVal(delta, "stop_reason"); sr != "" {
					stopReason = sr
				}
			}
			if u, ok := evt["usage"].(map[string]any); ok {
				mergeUsage(u)
			}
		}
	}
	if !sawPayload {
		return nil, fmt.Errorf("assemble: no Claude message events")
	}
	if id == "" {
		id = "msg_assembled"
	}
	if stopReason == "" {
		stopReason = "end_turn"
	}
	var content []any
	for _, idx := range order {
		acc := blocks[idx]
		if acc.isTool {
			input := map[string]any{}
			if s := claude.SanitizeToolArgs(claude.RepairToolArgs(acc.args.String())); s != "" {
				_ = json.Unmarshal([]byte(s), &input)
			}
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    acc.id,
				"name":  acc.name,
				"input": input,
			})
		} else if acc.text.Len() > 0 {
			content = append(content, map[string]any{"type": "text", "text": acc.text.String()})
		}
	}
	if content == nil {
		content = []any{}
	}
	return json.Marshal(map[string]any{
		"id":          id,
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"content":     content,
		"stop_reason": stopReason,
		"usage":       usage,
	})
}
