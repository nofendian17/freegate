package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// StreamState holds state for streaming translation.
type StreamState struct {
	// common
	seq        int
	responseID string
	created    int64
	started    bool
	completed  bool

	// openai -> responses
	msgTextBuf         map[string]string
	msgItemAdded       map[string]bool
	msgContentAdded    map[string]bool
	msgItemDone        map[string]bool
	reasoningID        string
	reasoningIdx       int
	reasoningBuf       string
	reasoningPartAdded bool
	reasoningDone      bool
	inThinking         bool
	funcArgsBuf        map[int]string
	funcNames          map[int]string
	funcCallIDs        map[int]string
	funcItemAdded      map[int]bool
	funcArgsDone       map[int]bool
	funcItemDone       map[int]bool

	// responses -> openai
	chatID            string
	toolCallIndex     int
	currentToolCallID string
	finishSent        bool

	sseBuf bytes.Buffer
}

func NewStreamState() *StreamState {
	return &StreamState{
		seq:             0,
		created:         0,
		msgTextBuf:      make(map[string]string),
		msgItemAdded:    make(map[string]bool),
		msgContentAdded: make(map[string]bool),
		msgItemDone:     make(map[string]bool),
		funcArgsBuf:     make(map[int]string),
		funcNames:       make(map[int]string),
		funcCallIDs:     make(map[int]string),
		funcItemAdded:   make(map[int]bool),
		funcArgsDone:    make(map[int]bool),
		funcItemDone:    make(map[int]bool),
	}
}

// Feed splits SSE buffer into complete blocks (delimited by \n\n)
func (s *StreamState) Feed(p []byte) []string {
	s.sseBuf.Write(p)
	data := s.sseBuf.String()
	var blocks []string
	for {
		idx := strings.Index(data, "\n\n")
		if idx < 0 {
			break
		}
		blocks = append(blocks, data[:idx])
		data = data[idx+2:]
	}
	s.sseBuf.Reset()
	s.sseBuf.WriteString(data)
	return blocks
}

// IsClosed reports if stream completed.
func (s *StreamState) IsClosed() bool { return s.completed }

// Helpers for OpenAI -> Responses

func (s *StreamState) nextSeq() int {
	s.seq++
	return s.seq
}

func formatResponsesEvent(event string, data map[string]any) string {
	b, _ := json.Marshal(data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(b))
}

func formatOpenAIChunk(id string, created int64, model string, delta map[string]any, finishReason string) string {
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{
			map[string]any{
				"index": 0,
				"delta": delta,
			},
		},
	}
	if finishReason != "" {
		ch := chunk["choices"].([]any)[0].(map[string]any)
		ch["finish_reason"] = finishReason
	}
	b, _ := json.Marshal(chunk)
	return fmt.Sprintf("data: %s\n\n", string(b))
}

// OpenAIChunkToResponses converts a single OpenAI SSE chunk map to Responses SSE events.
// Returns slice of formatted SSE blocks (each already includes event+data).
func (s *StreamState) OpenAIChunkToResponses(chunk map[string]any) []string {
	var events []string
	choicesRaw, _ := chunk["choices"].([]any)
	if len(choicesRaw) == 0 {
		return nil
	}
	choice, _ := choicesRaw[0].(map[string]any)
	if choice == nil {
		return nil
	}
	delta, _ := choice["delta"].(map[string]any)
	if delta == nil {
		delta = map[string]any{}
	}
	idx := 0
	if v, ok := choice["index"].(float64); ok {
		idx = int(v)
	}
	// init
	if !s.started {
		s.started = true
		if id, ok := chunk["id"].(string); ok && id != "" {
			s.responseID = "resp_" + id
		}
		if s.responseID == "" {
			s.responseID = fmt.Sprintf("resp_%d", s.created)
		}
		if s.created == 0 {
			if c, ok := chunk["created"].(float64); ok {
				s.created = int64(c)
			}
		}
		events = append(events, formatResponsesEvent("response.created", map[string]any{
			"type":            "response.created",
			"sequence_number": s.nextSeq(),
			"response": map[string]any{
				"id": s.responseID, "object": "response", "created_at": s.created, "status": "in_progress",
			},
		}))
		events = append(events, formatResponsesEvent("response.in_progress", map[string]any{
			"type":            "response.in_progress",
			"sequence_number": s.nextSeq(),
			"response":        map[string]any{"id": s.responseID, "object": "response", "created_at": s.created, "status": "in_progress"},
		}))
	}

	// reasoning
	if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
		events = append(events, s.startReasoning(idx)...)
		events = append(events, s.emitReasoningDelta(rc)...)
	} else if rc, ok := delta["reasoning"].(string); ok && rc != "" {
		events = append(events, s.startReasoning(idx)...)
		events = append(events, s.emitReasoningDelta(rc)...)
	}

	// content
	if content, ok := delta["content"].(string); ok && content != "" {
		c := content
		if strings.Contains(c, "<think>") {
			s.inThinking = true
			c = strings.ReplaceAll(c, "<think>", "")
			events = append(events, s.startReasoning(idx)...)
		}
		if strings.Contains(c, "</think>") {
			parts := strings.SplitN(c, "</think>", 2)
			if parts[0] != "" {
				events = append(events, s.emitReasoningDelta(parts[0])...)
			}
			events = append(events, s.closeReasoning()...)
			s.inThinking = false
			if len(parts) > 1 {
				c = parts[1]
			} else {
				c = ""
			}
		}
		if s.inThinking && c != "" {
			events = append(events, s.emitReasoningDelta(c)...)
		} else if c != "" {
			events = append(events, s.emitTextContent(idx, c)...)
		}
	}

	// tool_calls
	if tcList, ok := delta["tool_calls"].([]any); ok && len(tcList) > 0 {
		// close any open message
		for k := range s.msgItemAdded {
			events = append(events, s.closeMessage(k)...)
		}
		for _, tcRaw := range tcList {
			tc, _ := tcRaw.(map[string]any)
			if tc == nil {
				continue
			}
			events = append(events, s.emitToolCall(tc)...)
		}
	}

	// finish
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" && fr != "null" {
		for k := range s.msgItemAdded {
			events = append(events, s.closeMessage(k)...)
		}
		events = append(events, s.closeReasoning()...)
		for k := range s.funcCallIDs {
			events = append(events, s.closeToolCall(k)...)
		}
		events = append(events, s.sendCompleted()...)
	}

	return events
}

func (s *StreamState) startReasoning(idx int) []string {
	if s.reasoningID != "" {
		return nil
	}
	s.reasoningID = fmt.Sprintf("rs_%s_%d", s.responseID, idx)
	s.reasoningIdx = idx
	var ev []string
	ev = append(ev, formatResponsesEvent("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "sequence_number": s.nextSeq(), "output_index": idx,
		"item": map[string]any{"id": s.reasoningID, "type": "reasoning", "summary": []any{}},
	}))
	ev = append(ev, formatResponsesEvent("response.reasoning_summary_part.added", map[string]any{
		"type": "response.reasoning_summary_part.added", "sequence_number": s.nextSeq(),
		"item_id": s.reasoningID, "output_index": idx, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""},
	}))
	s.reasoningPartAdded = true
	return ev
}

func (s *StreamState) emitReasoningDelta(text string) []string {
	if text == "" {
		return nil
	}
	s.reasoningBuf += text
	return []string{formatResponsesEvent("response.reasoning_summary_text.delta", map[string]any{
		"type": "response.reasoning_summary_text.delta", "sequence_number": s.nextSeq(),
		"item_id": s.reasoningID, "output_index": s.reasoningIdx, "summary_index": 0, "delta": text,
	})}
}

func (s *StreamState) closeReasoning() []string {
	if s.reasoningID == "" || s.reasoningDone {
		return nil
	}
	s.reasoningDone = true
	var ev []string
	ev = append(ev, formatResponsesEvent("response.reasoning_summary_text.done", map[string]any{
		"type": "response.reasoning_summary_text.done", "sequence_number": s.nextSeq(),
		"item_id": s.reasoningID, "output_index": s.reasoningIdx, "summary_index": 0, "text": s.reasoningBuf,
	}))
	ev = append(ev, formatResponsesEvent("response.reasoning_summary_part.done", map[string]any{
		"type": "response.reasoning_summary_part.done", "sequence_number": s.nextSeq(),
		"item_id": s.reasoningID, "output_index": s.reasoningIdx, "summary_index": 0,
		"part": map[string]any{"type": "summary_text", "text": s.reasoningBuf},
	}))
	ev = append(ev, formatResponsesEvent("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": s.nextSeq(), "output_index": s.reasoningIdx,
		"item": map[string]any{"id": s.reasoningID, "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": s.reasoningBuf}}},
	}))
	return ev
}

func (s *StreamState) emitTextContent(idx int, content string) []string {
	var ev []string
	key := fmt.Sprintf("%d", idx)
	if !s.msgItemAdded[key] {
		s.msgItemAdded[key] = true
		ev = append(ev, formatResponsesEvent("response.output_item.added", map[string]any{
			"type": "response.output_item.added", "sequence_number": s.nextSeq(), "output_index": idx,
			"item": map[string]any{"id": fmt.Sprintf("msg_%s_%d", s.responseID, idx), "type": "message", "role": "assistant", "content": []any{}},
		}))
	}
	if !s.msgContentAdded[key] {
		s.msgContentAdded[key] = true
		ev = append(ev, formatResponsesEvent("response.content_part.added", map[string]any{
			"type": "response.content_part.added", "sequence_number": s.nextSeq(),
			"item_id": fmt.Sprintf("msg_%s_%d", s.responseID, idx), "output_index": idx, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": ""},
		}))
	}
	ev = append(ev, formatResponsesEvent("response.output_text.delta", map[string]any{
		"type": "response.output_text.delta", "sequence_number": s.nextSeq(),
		"item_id": fmt.Sprintf("msg_%s_%d", s.responseID, idx), "output_index": idx, "content_index": 0, "delta": content,
	}))
	s.msgTextBuf[key] += content
	return ev
}

func (s *StreamState) closeMessage(k string) []string {
	if !s.msgItemAdded[k] || s.msgItemDone[k] {
		return nil
	}
	s.msgItemDone[k] = true
	text := s.msgTextBuf[k]
	msgID := fmt.Sprintf("msg_%s_%s", s.responseID, k)
	var ev []string
	ev = append(ev, formatResponsesEvent("response.output_text.done", map[string]any{
		"type": "response.output_text.done", "sequence_number": s.nextSeq(),
		"item_id": msgID, "output_index": parseIdx(k), "content_index": 0, "text": text,
	}))
	ev = append(ev, formatResponsesEvent("response.content_part.done", map[string]any{
		"type": "response.content_part.done", "sequence_number": s.nextSeq(),
		"item_id": msgID, "output_index": parseIdx(k), "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": text},
	}))
	ev = append(ev, formatResponsesEvent("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": s.nextSeq(), "output_index": parseIdx(k),
		"item": map[string]any{"id": msgID, "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}},
	}))
	return ev
}

func (s *StreamState) emitToolCall(tc map[string]any) []string {
	var idx int
	if v, ok := tc["index"].(float64); ok {
		idx = int(v)
	}
	fn, _ := tc["function"].(map[string]any)
	name, _ := fn["name"].(string)
	if name != "" {
		s.funcNames[idx] = name
	}
	if id, ok := tc["id"].(string); ok && id != "" {
		s.funcCallIDs[idx] = id
	}
	callID := s.funcCallIDs[idx]
	if !s.funcItemAdded[idx] && callID != "" && s.funcNames[idx] != "" {
		s.funcItemAdded[idx] = true
		return []string{formatResponsesEvent("response.output_item.added", map[string]any{
			"type": "response.output_item.added", "sequence_number": s.nextSeq(), "output_index": idx,
			"item": map[string]any{"id": "fc_" + callID, "type": "function_call", "call_id": callID, "name": s.funcNames[idx], "arguments": ""},
		})}
	}
	// args delta
	if args, ok := fn["arguments"].(string); ok && args != "" {
		s.funcArgsBuf[idx] += args
		if s.funcItemAdded[idx] {
			return []string{formatResponsesEvent("response.function_call_arguments.delta", map[string]any{
				"type": "response.function_call_arguments.delta", "sequence_number": s.nextSeq(),
				"item_id": "fc_" + callID, "output_index": idx, "delta": args,
			})}
		}
	}
	return nil
}

func (s *StreamState) closeToolCall(idx int) []string {
	callID := s.funcCallIDs[idx]
	if callID == "" || s.funcItemDone[idx] {
		return nil
	}
	args := s.funcArgsBuf[idx]
	if args == "" {
		args = "{}"
	}
	s.funcItemDone[idx] = true
	var ev []string
	ev = append(ev, formatResponsesEvent("response.function_call_arguments.done", map[string]any{
		"type": "response.function_call_arguments.done", "sequence_number": s.nextSeq(),
		"item_id": "fc_" + callID, "output_index": idx, "arguments": args,
	}))
	ev = append(ev, formatResponsesEvent("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": s.nextSeq(), "output_index": idx,
		"item": map[string]any{"id": "fc_" + callID, "type": "function_call", "call_id": callID, "name": s.funcNames[idx], "arguments": args},
	}))
	return ev
}

func (s *StreamState) sendCompleted() []string {
	if s.completed {
		return nil
	}
	s.completed = true
	return []string{formatResponsesEvent("response.completed", map[string]any{
		"type": "response.completed", "sequence_number": s.nextSeq(),
		"response": map[string]any{"id": s.responseID, "object": "response", "created_at": s.created, "status": "completed"},
	})}
}

// Responses -> OpenAI direction

func (s *StreamState) ResponsesEventToOpenAI(eventName string, data map[string]any) []string {
	if !s.started {
		s.started = true
		s.chatID = fmt.Sprintf("chatcmpl-%d", s.created)
		if s.created == 0 {
			s.created = 1
		}
	}

	switch eventName {
	case "response.output_text.delta":
		delta, _ := data["delta"].(string)
		if delta == "" {
			return nil
		}
		return []string{formatOpenAIChunk(s.chatID, s.created, "muse-spark", map[string]any{"content": delta}, "")}
	case "response.reasoning_summary_text.delta":
		delta, _ := data["delta"].(string)
		if delta == "" {
			return nil
		}
		return []string{formatOpenAIChunk(s.chatID, s.created, "muse-spark", map[string]any{"reasoning_content": delta}, "")}
	case "response.output_item.added":
		item, _ := data["item"].(map[string]any)
		if item == nil {
			return nil
		}
		typ, _ := item["type"].(string)
		if typ == "function_call" || typ == "custom_tool_call" {
			callID, _ := item["call_id"].(string)
			if callID == "" {
				callID = fmt.Sprintf("call_%d", s.toolCallIndex)
			}
			s.currentToolCallID = callID
			name, _ := item["name"].(string)
			return []string{formatOpenAIChunk(s.chatID, s.created, "muse-spark", map[string]any{
				"tool_calls": []any{map[string]any{"index": s.toolCallIndex, "id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": ""}}},
			}, "")}
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		delta, _ := data["delta"].(string)
		if delta == "" {
			return nil
		}
		return []string{formatOpenAIChunk(s.chatID, s.created, "muse-spark", map[string]any{
			"tool_calls": []any{map[string]any{"index": s.toolCallIndex, "function": map[string]any{"arguments": delta}}},
		}, "")}
	case "response.output_item.done":
		item, _ := data["item"].(map[string]any)
		if item != nil {
			if typ, _ := item["type"].(string); typ == "function_call" || typ == "custom_tool_call" {
				s.toolCallIndex++
			}
		}
	case "response.completed", "response.done":
		if s.finishSent {
			return nil
		}
		s.finishSent = true
		finish := "stop"
		if s.toolCallIndex > 0 {
			finish = "tool_calls"
		}
		return []string{formatOpenAIChunk(s.chatID, s.created, "muse-spark", map[string]any{}, finish)}
	case "response.failed", "error":
		if s.finishSent {
			return nil
		}
		s.finishSent = true
		var msg string
		if err, ok := data["error"].(map[string]any); ok {
			msg, _ = err["message"].(string)
		}
		if msg == "" {
			msg = "upstream error"
		}
		return []string{formatOpenAIChunk(s.chatID, s.created, "muse-spark", map[string]any{"content": "[Error] " + msg}, "stop")}
	}
	return nil
}

func parseIdx(k string) int {
	var i int
	_, _ = fmt.Sscanf(k, "%d", &i)
	return i
}
