package claude

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// ClaudeToOpenAIState tracks per-stream state for the Claude → OpenAI
// streaming translation. Used when the upstream speaks native Claude
// SSE and the client expects OpenAI chat-completion chunks.
//
// Mirrors the state model of 9router's response/claude-to-openai.js
// (minus the Antigravity tool-name restoration and built-in server_tool_use
// skipping — neither is in scope here).
type ClaudeToOpenAIState struct {
	messageID     string
	model         string
	created       int64
	toolCallIndex int
	toolCalls     map[int]*c2oToolCall // key: Claude block index
	textStarted   bool
	thinkingOpen  bool
	inThinking    bool
	thinkingBlock *int
	usage         *c2oUsage
	finishReason  string
	finishSent    bool
	sseBuf        bytes.Buffer
}

type c2oToolCall struct {
	Index int    // OpenAI tool_call.index
	ID    string // OpenAI tool_call.id
	Name  string // OpenAI tool_call.function.name
	Args  strings.Builder
}

type c2oUsage struct {
	PromptTokens      int64
	CompletionTokens  int64
	TotalTokens       int64
	CacheReadTokens   int64
	CacheCreateTokens int64
}

// NewClaudeToOpenAIState constructs a new state with a random message id
// and a creation timestamp.
func NewClaudeToOpenAIState() *ClaudeToOpenAIState {
	return &ClaudeToOpenAIState{
		messageID: "msg_" + randID(12),
		created:   time.Now().Unix(),
		toolCalls: make(map[int]*c2oToolCall),
	}
}

// Feed appends incoming bytes and returns complete newline-terminated
// lines. Partial trailing data is retained for the next call.
func (s *ClaudeToOpenAIState) Feed(p []byte) []string {
	s.sseBuf.Write(p)
	data := s.sseBuf.String()
	var lines []string
	for {
		idx := strings.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		lines = append(lines, data[:idx])
		data = data[idx+1:]
	}
	s.sseBuf.Reset()
	s.sseBuf.WriteString(data)
	return lines
}

// ProcessChunk converts one parsed Claude SSE event into the OpenAI
// chat.completion.chunk SSE line(s) to emit. Returns nil if nothing
// should be emitted for this event.
//
// Each emitted line is a complete SSE record: "data: <json>\n\n".
func (s *ClaudeToOpenAIState) ProcessChunk(chunk map[string]any) []string {
	event, _ := chunk["type"].(string)

	// Once finish has been sent, ignore all subsequent events.
	// message_stop is already idempotent via its own finishSent guard.
	if s.finishSent && event != "message_stop" {
		return nil
	}

	switch event {
	case "message_start":
		return s.onMessageStart(chunk)
	case "content_block_start":
		return s.onContentBlockStart(chunk)
	case "content_block_delta":
		return s.onContentBlockDelta(chunk)
	case "content_block_stop":
		return s.onContentBlockStop(chunk)
	case "message_delta":
		return s.onMessageDelta(chunk)
	case "message_stop":
		return s.onMessageStop()
	}
	return nil
}

func (s *ClaudeToOpenAIState) onMessageStart(chunk map[string]any) []string {
	if msg, ok := chunk["message"].(map[string]any); ok {
		if id, ok := msg["id"].(string); ok && id != "" {
			s.messageID = id
		}
		if m, ok := msg["model"].(string); ok && m != "" {
			s.model = m
		}
	}
	return []string{s.chunkLine(map[string]any{"role": "assistant"}, nil)}
}

func (s *ClaudeToOpenAIState) onContentBlockStart(chunk map[string]any) []string {
	block, _ := chunk["content_block"].(map[string]any)
	btype, _ := block["type"].(string)
	switch btype {
	case "text":
		s.textStarted = true
		// No chunk emitted at start; text deltas follow.
		return nil
	case "thinking":
		s.inThinking = true
		idx := asInt(chunk["index"])
		s.thinkingBlock = &idx
		// Emit the <think> marker as ordinary content.
		return []string{s.chunkLine(map[string]any{"content": "<think>"}, nil)}
	case "tool_use":
		id, _ := block["id"].(string)
		name, _ := block["name"].(string)
		blockIdx := asInt(chunk["index"])
		tc := &c2oToolCall{Index: s.toolCallIndex, ID: id, Name: name}
		s.toolCalls[blockIdx] = tc
		s.toolCallIndex++
		return []string{s.chunkLine(map[string]any{
			"tool_calls": []any{map[string]any{
				"index": tc.Index,
				"id":    tc.ID,
				"type":  "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": "",
				},
			}},
		}, nil)}
	}
	return nil
}

func (s *ClaudeToOpenAIState) onContentBlockDelta(chunk map[string]any) []string {
	delta, _ := chunk["delta"].(map[string]any)
	dtype, _ := delta["type"].(string)
	switch dtype {
	case "text_delta":
		text, _ := delta["text"].(string)
		if text == "" {
			return nil
		}
		return []string{s.chunkLine(map[string]any{"content": text}, nil)}
	case "thinking_delta":
		think, _ := delta["thinking"].(string)
		if think == "" {
			return nil
		}
		return []string{s.chunkLine(map[string]any{"reasoning_content": think}, nil)}
	case "input_json_delta":
		pj, _ := delta["partial_json"].(string)
		if pj == "" {
			return nil
		}
		blockIdx := asInt(chunk["index"])
		tc, ok := s.toolCalls[blockIdx]
		if !ok {
			return nil
		}
		// Buffer only; the repaired arguments are emitted as a single delta
		// on content_block_stop. Emitting fragments verbatim lets the client
		// join a duplicated/concatenated object (X}{Y), which fails with
		// "could not be parsed as JSON".
		tc.Args.WriteString(pj)
		return nil
	}
	return nil
}

func (s *ClaudeToOpenAIState) onContentBlockStop(chunk map[string]any) []string {
	blockIdx := asInt(chunk["index"])
	// Flush buffered + repaired tool arguments as a single delta so the
	// client receives one valid JSON object (no duplicated/concatenated X}{Y).
	if tc, ok := s.toolCalls[blockIdx]; ok && tc.Args.Len() > 0 {
		repaired := repairToolArgs(tc.Args.String())
		tc.Args.Reset()
		return []string{s.chunkLine(map[string]any{
			"tool_calls": []any{map[string]any{
				"index":    tc.Index,
				"function": map[string]any{"arguments": repaired},
			}},
		}, nil)}
	}
	if s.inThinking {
		idx := asInt(chunk["index"])
		if s.thinkingBlock != nil && *s.thinkingBlock == idx {
			s.inThinking = false
			s.thinkingBlock = nil
			return []string{s.chunkLine(map[string]any{"content": "</think>"}, nil)}
		}
	}
	s.textStarted = false
	s.thinkingOpen = false
	return nil
}

func (s *ClaudeToOpenAIState) onMessageDelta(chunk map[string]any) []string {
	if s.finishSent {
		return nil
	}
	var results []string
	if usage, ok := chunk["usage"].(map[string]any); ok {
		s.usage = parseClaudeUsage(usage)
	}
	if delta, ok := chunk["delta"].(map[string]any); ok {
		if sr, ok := delta["stop_reason"].(string); ok && sr != "" {
			s.finishReason = convertStopReasonOpenAI(sr)
			results = append(results, s.finalChunk())
			s.finishSent = true
		}
	}
	return results
}

func (s *ClaudeToOpenAIState) onMessageStop() []string {
	if s.finishSent {
		return nil
	}
	fr := s.finishReason
	if fr == "" {
		if len(s.toolCalls) > 0 {
			fr = "tool_calls"
		} else {
			fr = "stop"
		}
	}
	s.finishSent = true
	return []string{s.chunkLine(map[string]any{}, &fr)}
}

func (s *ClaudeToOpenAIState) chunkLine(delta map[string]any, finishReason *string) string {
	choice := map[string]any{
		"index": 0,
		"delta": delta,
	}
	if finishReason != nil {
		choice["finish_reason"] = *finishReason
	} else {
		choice["finish_reason"] = nil
	}
	chunk := map[string]any{
		"id":      "chatcmpl-" + s.messageID,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []any{choice},
	}
	data, err := json.Marshal(chunk)
	if err != nil {
		return ""
	}
	return "data: " + string(data) + "\n\n"
}

func (s *ClaudeToOpenAIState) finalChunk() string {
	choice := map[string]any{
		"index":         0,
		"delta":         map[string]any{},
		"finish_reason": s.finishReason,
	}
	chunk := map[string]any{
		"id":      "chatcmpl-" + s.messageID,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []any{choice},
	}
	if s.usage != nil {
		u := map[string]any{
			"prompt_tokens":     s.usage.PromptTokens,
			"completion_tokens": s.usage.CompletionTokens,
			"total_tokens":      s.usage.TotalTokens,
		}
		if s.usage.CacheReadTokens > 0 || s.usage.CacheCreateTokens > 0 {
			details := map[string]any{}
			if s.usage.CacheReadTokens > 0 {
				details["cached_tokens"] = s.usage.CacheReadTokens
			}
			if s.usage.CacheCreateTokens > 0 {
				details["cache_creation_tokens"] = s.usage.CacheCreateTokens
			}
			u["prompt_tokens_details"] = details
		}
		chunk["usage"] = u
	} else {
		chunk["usage"] = map[string]any{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		}
	}
	data, err := json.Marshal(chunk)
	if err != nil {
		return ""
	}
	return "data: " + string(data) + "\n\n"
}

// parseClaudeUsage converts a Claude message_delta usage into the
// OpenAI usage fields, where prompt_tokens = input_tokens +
// cache_read + cache_creation (all prompt-side tokens).
//
// Accepts float64 (the JSON-decoded type) and int / int64 (when callers
// construct the map by hand in tests or upstream code).
func parseClaudeUsage(u map[string]any) *c2oUsage {
	out := &c2oUsage{}
	if v, ok := asInt64(u["input_tokens"]); ok {
		out.PromptTokens = v
	}
	if v, ok := asInt64(u["output_tokens"]); ok {
		out.CompletionTokens = v
	}
	if v, ok := asInt64(u["cache_read_input_tokens"]); ok {
		out.CacheReadTokens = v
	}
	if v, ok := asInt64(u["cache_creation_input_tokens"]); ok {
		out.CacheCreateTokens = v
	}
	out.PromptTokens = out.PromptTokens + out.CacheReadTokens + out.CacheCreateTokens
	out.TotalTokens = out.PromptTokens + out.CompletionTokens
	return out
}

// asInt64 coerces a JSON-decoded numeric value (typically float64) to
// int64. Also accepts int and int64 for callers that build maps by hand.
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

func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
