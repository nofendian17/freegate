package claude

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// --- Streaming state ---

// StreamState tracks state for OpenAI → Claude streaming translation.
type StreamState struct {
	messageStartSent bool
	messageID        string
	model            string
	nextBlockIdx     int
	textOpen         bool
	textBlockIdx     int
	thinkingOpen     bool
	thinkingIdx      int
	toolCalls        map[int]*toolCallInfo
	toolArgBufs      map[int]*bytes.Buffer
	openToolBlocks   []int
	usage            *usageInfo
	finishReason     string
	finishSent       bool
	closed           bool
	sseBuf           bytes.Buffer
	outputContent    strings.Builder
	seenIDs          map[string]int
}

type toolCallInfo struct {
	ID    string
	Name  string
	Index int
}

type usageInfo struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CacheReadTokens   int64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreateTokens int64 `json:"cache_creation_input_tokens,omitempty"`
}

// NewStreamState creates a new Claude stream state.
func NewStreamState() *StreamState {
	return &StreamState{
		messageID:    "msg_" + randID(8),
		nextBlockIdx: 0,
		toolCalls:    make(map[int]*toolCallInfo),
		toolArgBufs:  make(map[int]*bytes.Buffer),
		seenIDs:      make(map[string]int),
	}
}

// Feed appends incoming bytes and returns complete lines (newline-terminated).
// Partial trailing data is retained for the next call.
func (s *StreamState) Feed(p []byte) []string {
	s.sseBuf.Write(p)
	return s.drainLines()
}

func (s *StreamState) drainLines() []string {
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

// IsClosed reports whether the stream has finished.
func (s *StreamState) IsClosed() bool {
	return s.closed
}

// MarkClosed marks the stream as closed.
func (s *StreamState) MarkClosed() {
	s.closed = true
}

// IsStartSent reports whether message_start has been emitted.
func (s *StreamState) IsStartSent() bool {
	return s.messageStartSent
}

// MarkStartSent marks message_start as emitted.
func (s *StreamState) MarkStartSent() {
	s.messageStartSent = true
}

// --- Chunk processing ---

// ProcessChunk converts an OpenAI SSE chunk to Claude SSE events.
// It mutates state as a side effect.
func ProcessChunk(chunk map[string]any, state *StreamState) []string {
	var events []string

	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		// Might be a usage-only chunk or error
		if usage, ok := chunk["usage"].(map[string]any); ok && !state.messageStartSent {
			// Usage in first chunk
			state.usage = extractUsage(usage)
			state.MarkStartSent()
			events = append(events, formatSSE("message_start", map[string]any{
				"type":    "message_start",
				"message": buildClaudeMessage(state),
			})...)
			return events
		}
		return nil
	}

	choice, _ := choices[0].(map[string]any)
	if choice == nil {
		return nil
	}

	delta, _ := choice["delta"].(map[string]any)
	if delta == nil {
		// Might be finish_reason in choice directly (some providers)
		delta = map[string]any{}
	}

	// Emit message_start on first relevant delta (only before finish)
	if !state.messageStartSent && !state.finishSent {
		events = append(events, formatSSE("message_start", map[string]any{
			"type":    "message_start",
			"message": buildClaudeMessage(state),
		})...)
		state.MarkStartSent()
	}

	// Handle reasoning_content (Claude thinking) — prefer reasoning_content;
	// fall back to reasoning only when reasoning_content is absent.
	if !state.finishSent {
		if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
			events = append(events, handleReasoningContent(rc, state)...)
		} else if r, ok := delta["reasoning"].(string); ok && r != "" {
			events = append(events, handleReasoningContent(r, state)...)
		}

		// Handle text content
		if txt, ok := delta["content"].(string); ok && txt != "" {
			events = append(events, handleTextContent(txt, state)...)
		}

		// Handle tool calls
		if tcList, ok := delta["tool_calls"].([]any); ok && len(tcList) > 0 {
			events = append(events, handleToolCalls(tcList, state)...)
		}
	}

	// Handle usage
	if usage, ok := chunk["usage"].(map[string]any); ok {
		state.usage = extractUsage(usage)
	}

	// Handle finish_reason
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" && fr != "null" && !state.finishSent {
		state.finishReason = fr
		events = append(events, handleFinish(state)...)
		state.finishSent = true
		state.closed = true
	}

	return events
}

// --- Event generators ---

// closeOpenToolBlocks emits content_block_stop for every tool_use block
// still open and resets the tool-call tracking so a later tool call starts
// fresh. Anthropic requires content blocks to be strictly sequential, so
// before opening a text or thinking block we must close any open tool_use
// block first.
func (s *StreamState) closeOpenToolBlocks() []string {
	var events []string
	for _, blockIdx := range s.openToolBlocks {
		events = append(events, contentBlockStop(blockIdx)...)
	}
	s.openToolBlocks = nil
	s.toolCalls = make(map[int]*toolCallInfo)
	s.toolArgBufs = make(map[int]*bytes.Buffer)
	return events
}

func handleReasoningContent(text string, state *StreamState) []string {
	var events []string

	// Close any open text block first
	if state.textOpen {
		events = append(events, contentBlockStop(state.textBlockIdx)...)
		state.textOpen = false
	}

	// Close any open tool_use block before opening thinking
	if len(state.openToolBlocks) > 0 {
		events = append(events, state.closeOpenToolBlocks()...)
	}

	// Open thinking block if not open
	if !state.thinkingOpen {
		state.thinkingOpen = true
		state.thinkingIdx = state.nextBlockIdx
		state.nextBlockIdx++
		events = append(events, formatSSE("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": state.thinkingIdx,
			"content_block": map[string]any{
				"type": "thinking",
			},
		})...)
	}

	state.outputContent.WriteString(text)

	events = append(events, formatSSE("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": state.thinkingIdx,
		"delta": map[string]any{
			"type":     "thinking_delta",
			"thinking": text,
		},
	})...)

	return events
}

func handleTextContent(text string, state *StreamState) []string {
	var events []string

	// Close any open thinking block
	if state.thinkingOpen {
		events = append(events, contentBlockStop(state.thinkingIdx)...)
		state.thinkingOpen = false
	}

	// Close any open tool_use block before opening text
	if len(state.openToolBlocks) > 0 {
		events = append(events, state.closeOpenToolBlocks()...)
	}

	// Open text block if not open
	if !state.textOpen {
		state.textOpen = true
		state.textBlockIdx = state.nextBlockIdx
		state.nextBlockIdx++
		events = append(events, formatSSE("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": state.textBlockIdx,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		})...)
	}

	state.outputContent.WriteString(text)

	events = append(events, formatSSE("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": state.textBlockIdx,
		"delta": map[string]any{
			"type": "text_delta",
			"text": text,
		},
	})...)

	return events
}

func handleToolCalls(tcList []any, state *StreamState) []string {
	var events []string

	for _, tcAny := range tcList {
		tc, ok := tcAny.(map[string]any)
		if !ok {
			continue
		}

		idx, _ := tc["index"].(float64)
		intIdx := int(idx)

		if id, ok := tc["id"].(string); ok && id != "" {
			// If we have already seen/initialized this tool call index,
			// do NOT start a new block or generate a new ID. Just use the existing one.
			if existing, exists := state.toolCalls[intIdx]; exists {
				id = existing.ID
			} else {
				// Ensure tool use ID is unique in this response stream
				if count, seen := state.seenIDs[id]; seen {
					state.seenIDs[id] = count + 1
					id = fmt.Sprintf("%s_%d", id, count)
			} else {
				state.seenIDs[id] = 1
			}

				// New tool call — close text/thinking, open tool_use block
				if state.textOpen {
					events = append(events, contentBlockStop(state.textBlockIdx)...)
					state.textOpen = false
				}
				if state.thinkingOpen {
					events = append(events, contentBlockStop(state.thinkingIdx)...)
					state.thinkingOpen = false
				}

				fn, _ := tc["function"].(map[string]any)
				name, _ := fn["name"].(string)

				blockIdx := state.nextBlockIdx
				state.nextBlockIdx++
				state.toolCalls[intIdx] = &toolCallInfo{
					ID:    id,
					Name:  name,
					Index: blockIdx,
				}
				state.toolArgBufs[intIdx] = &bytes.Buffer{}
				state.openToolBlocks = append(state.openToolBlocks, blockIdx)

				events = append(events, formatSSE("content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": blockIdx,
					"content_block": map[string]any{
						"type": "tool_use",
						"id":   id,
						"name": name,
					},
				})...)
			}
		}

		// Accumulate arguments.
		// Some models (e.g. tencent/hy3) emit `arguments` as a JSON object
		// inline rather than as a string. Handle both forms.
		if fn, ok := tc["function"].(map[string]any); ok {
			var args string
			if argsStr, ok := fn["arguments"].(string); ok {
				args = argsStr
			} else if argsObj, ok := fn["arguments"].(map[string]any); ok {
				if b, err := json.Marshal(argsObj); err == nil {
					args = string(b)
				}
			}
			if args != "" {
				ti := state.toolCalls[intIdx]
				if buf, ok := state.toolArgBufs[intIdx]; ok && buf != nil {
					buf.WriteString(args)
				}
				state.outputContent.WriteString(args)
				// We no longer emit input_json_delta here. Instead, we buffer
				// the raw string and emit a single repaired delta in handleFinish.
				// This avoids premature truncation and streaming syntax errors.
				_ = ti
			}
		}
	}

	return events
}



// splitToolArgs splits a concatenated tool-arguments string (e.g.
// {"a":1}{"b":2}) into individual JSON objects by walking the raw string
// and finding top-level '}{' boundaries. Each fragment is run through
// repairToolArgs individually. Returns at least one object (the first valid
// object if no split is possible).
func splitToolArgs(s string) []string {
	if s == "" {
		return []string{`{}`}
	}
	depth := 0
	start := 0
	inStr := false
	escaped := false
	var parts []string
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch ch {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && i+1 < len(s) && s[i+1] == '{' {
				// Top-level }{ boundary — splice here.
				part := repairToolArgs(s[start : i+1])
				if part != "" {
					parts = append(parts, part)
				}
				depth = 0
				i++ // skip past the '{' after '}'
				start = i
			}
		}
	}
	// Collect the last (or only) segment.
	if start < len(s) {
		part := repairToolArgs(s[start:])
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		parts = []string{repairToolArgs(s)}
	}
	return parts
}

// repairToolArgs attempts to return valid JSON from an accumulated tool-call
// argument buffer. It handles three failure modes:
//
//  1. Concatenated duplicate objects: {"a":1}{"a":1} → decodes only the first.
//  2. Unescaped inner quotes in string values: {"cmd":"echo "$F""} →
//     escapes the inner quotes so the result is valid JSON.
//  3. Literal newlines and control characters inside strings.
//
// The result is ALWAYS a JSON object (or "{}"). Models such as tencent/hy3-free
// sometimes emit the arguments as a bare JSON string, an array, or a string that
// itself encodes an object ("{\"cmd\":\"ls\"}"); emitting those verbatim makes
// the client reject the tool_use with "input JSON failed to parse", so they are
// normalized to "{}".
func repairToolArgs(s string) string {
	if s == "" {
		return "{}"
	}
	// Fast path: already valid JSON.
	var dummy any
	if json.Unmarshal([]byte(s), &dummy) == nil {
		return ensureObjectJSON(dummy)
	}
	// Always repair quotes and control characters first!
	fixed := repairUnescapedQuotes(s)

	// Close any unterminated containers ({"a":1 or {"a":{"b":1), then strip
	// any trailing commas the closure may have exposed ({"a":1,}).
	if closed := repairUnterminated(fixed); closed != "" {
		fixed = closed
	}
	fixed = repairTrailingCommas(fixed)

	// Try parsing exactly one object (handles concatenation) from the repaired string!
	// UseNumber keeps integer precision (no float64 round-trip corruption).
	dec := json.NewDecoder(strings.NewReader(fixed))
	dec.UseNumber()
	if dec.Decode(&dummy) == nil {
		return ensureObjectJSON(dummy)
	}

	// Give up — but never hand the client unparseable JSON, which Claude Code
	// rejects with "input JSON failed to parse". An empty object parses
	// cleanly; the args are lost but the tool call survives (matches mustJSON).
	return "{}"
}

// ensureObjectJSON returns the JSON encoding of v when v is a JSON object.
// If v is a string, it is treated as a possibly double-encoded value and
// parsed once more, so a model that emits "{\"cmd\":\"ls\"}" (a JSON string
// wrapping an object) is unwrapped to {"cmd":"ls"}. Any other shape
// (bare string, array, number, bool, null) is normalized to "{}" because
// tool_use input must be a JSON object.
func ensureObjectJSON(v any) string {
	switch val := v.(type) {
	case map[string]any:
		if b, err := json.Marshal(val); err == nil {
			return string(b)
		}
	case string:
		var inner any
		if json.Unmarshal([]byte(val), &inner) == nil {
			if b, err := json.Marshal(inner); err == nil {
				return string(b)
			}
		}
	}
	return "{}"
}

// repairUnescapedQuotes scans a JSON string byte-by-byte and escapes any '"'
// inside a string value that is not followed by a valid JSON structural character.
func repairUnescapedQuotes(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	inStr := false
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			out.WriteByte(ch)
			continue
		}
		if ch == '\\' {
			escaped = true
			out.WriteByte(ch)
			continue
		}
		if ch == '"' {
			if !inStr {
				inStr = true
				out.WriteByte(ch)
			} else {
				// Look ahead for a valid structural character.
				j := i + 1
				for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\r' || s[j] == '\n') {
					j++
				}
				validClose := j >= len(s)
				if !validClose {
					next := s[j]
					validClose = next == ':' || next == ',' || next == '}' || next == ']'
				}
				if validClose {
					inStr = false
					out.WriteByte(ch)
				} else {
					out.WriteByte('\\')
					out.WriteByte('"')
				}
			}
			continue
		}

		if inStr {
			if ch == '\n' {
				out.WriteByte('\\')
				out.WriteByte('n')
				continue
			} else if ch == '\r' {
				out.WriteByte('\\')
				out.WriteByte('r')
				continue
			} else if ch == '\t' {
				out.WriteByte('\\')
				out.WriteByte('t')
				continue
			} else if ch < 0x20 {
				out.WriteString(fmt.Sprintf("\\u%04x", ch))
				continue
			}
		}

		out.WriteByte(ch)
	}
	return out.String()
}

// repairTrailingCommas drops a comma that immediately precedes a closing
// '}' or ']' (with optional whitespace), e.g. {"a":1,} or [2,3,].
func repairTrailingCommas(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != ',' {
			out.WriteByte(s[i])
			continue
		}
		j := i + 1
		for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
			j++
		}
		if j < len(s) && (s[j] == '}' || s[j] == ']') {
			continue // drop the trailing comma
		}
		out.WriteByte(',')
	}
	return out.String()
}

// repairUnterminated appends the closing '"' (for an unterminated string
// literal), then the '}' / ']' needed to balance any unclosed containers,
// respecting string literals. Returns "" when already balanced (nothing to
// fix). Closing an open string first matters: a truncated argument like
// {"command":"rm -rf /tmp/foo would otherwise be balanced to the invalid
// {"command":"rm -rf /tmp/foo} (string never closed) and rejected downstream.
func repairUnterminated(s string) string {
	depth := 0
	var closers []byte
	inStr := false
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch ch {
		case '{', '[':
			depth++
			closers = append(closers, ch)
		case '}', ']':
			if depth > 0 {
				depth--
				closers = closers[:len(closers)-1]
			}
		}
	}
	if depth == 0 && !inStr {
		return ""
	}
	// Don't emit a closing quote right after a trailing backslash: it would be
	// escaped by the parser and fail to terminate the string.
	trailingEscape := len(s) > 0 && s[len(s)-1] == '\\'
	var sb strings.Builder
	sb.WriteString(s)
	if inStr && !trailingEscape {
		sb.WriteByte('"')
	}
	for i := len(closers) - 1; i >= 0; i-- {
		if closers[i] == '{' {
			sb.WriteByte('}')
		} else {
			sb.WriteByte(']')
		}
	}
	return sb.String()
}

func handleFinish(state *StreamState) []string {
	var events []string

	// Close open blocks
	if state.textOpen {
		events = append(events, contentBlockStop(state.textBlockIdx)...)
		state.textOpen = false
	}
	if state.thinkingOpen {
		events = append(events, contentBlockStop(state.thinkingIdx)...)
		state.thinkingOpen = false
	}

	// Close any open tool_use blocks, repairing each accumulated arg buffer before stop.
	if len(state.openToolBlocks) > 0 {
		for intIdx, ti := range state.toolCalls {
			if buf, ok := state.toolArgBufs[intIdx]; ok && buf != nil && buf.Len() > 0 {
				accumulated := buf.String()
				repaired := repairToolArgs(accumulated)
				
				// Emit a single delta containing the fully repaired arguments JSON
				events = append(events, formatSSE("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": ti.Index,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": repaired,
					},
				})...)
			}
		}
		events = append(events, state.closeOpenToolBlocks()...)
	}

	// Map finish_reason
	stopReason := mapFinishReason(state.finishReason)

	msgDelta := map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
	}
	if state.usage != nil {
		msgDelta["usage"] = state.usage
	} else {
		estimatedTokens := int64(state.outputContent.Len() / 4)
		if estimatedTokens == 0 && state.outputContent.Len() > 0 {
			estimatedTokens = 1
		}
		msgDelta["usage"] = &usageInfo{
			OutputTokens: estimatedTokens,
		}
	}

	events = append(events, formatSSE("message_delta", msgDelta)...)
	events = append(events, formatSSE("message_stop", map[string]any{
		"type": "message_stop",
	})...)

	return events
}

func contentBlockStop(index int) []string {
	return formatSSE("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": index,
	})
}

// --- SSE helpers ---

// sseBuffer accumulates bytes until a complete SSE message (\n\n) is available.
type sseBuffer struct {
	buf bytes.Buffer
}

func (sb *sseBuffer) Feed(p []byte) []string {
	sb.buf.Write(p)
	return sb.Drain()
}

func (sb *sseBuffer) Drain() []string {
	data := sb.buf.String()
	var lines []string
	for {
		idx := strings.Index(data, "\n\n")
		if idx < 0 {
			break
		}
		block := data[:idx]
		lines = append(lines, block)
		data = data[idx+2:]
	}
	sb.buf.Reset()
	sb.buf.WriteString(data)
	return lines
}

func buildClaudeMessage(state *StreamState) map[string]any {
	msg := map[string]any{
		"id":            state.messageID,
		"type":          "message",
		"role":          "assistant",
		"content":       []any{},
		"model":         state.model,
		"stop_reason":   nil,
		"stop_sequence": nil,
	}
	if state.usage != nil {
		msg["usage"] = state.usage
	} else {
		msg["usage"] = &usageInfo{}
	}
	return msg
}

func formatSSE(event string, data map[string]any) []string {
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	return []string{
		"event: " + event + "\n",
		"data: " + string(dataBytes) + "\n\n",
	}
}

// --- Random ID generation ---

func randID(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[idx.Int64()]
	}
	return string(b)
}
