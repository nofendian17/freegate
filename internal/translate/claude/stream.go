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
	pendingContent   []pendingContent
	// holdBuf retains recent text deltas for orphan-invoke recovery
	// (vllm#48931/#55954): a complete DSML invoke block split across
	// deltas is only recognizable once fully arrived. Only the trailing
	// window is held, so time-to-first-token is unaffected for normal
	// prose; see holdTextContent.
	holdBuf strings.Builder
	// orphanRecovered marks that this turn recovered at least one tool
	// call from an orphan invoke block. Trailing prose after a recovered
	// call is dropped at finish (a tool call ends the turn).
	orphanRecovered bool
	// nativeToolSeen marks upstream tool_calls deltas. Native activity
	// bypasses the hold entirely (native tool_calls always win) and
	// preserves the pend-behind-open-blocks behavior for interleaved
	// text; orphan-only turns keep flowing through the hold so a later
	// parallel invoke still recovers.
	nativeToolSeen bool
	usage          *usageInfo
	finishReason   string
	finishSent     bool
	closed         bool
	sseBuf         bytes.Buffer
	outputContent  strings.Builder
	seenIDs        map[string]int
}

// Hold-back bound for orphan-invoke recovery: a suspect '<' region past
// this size flushes as plain text (same as today's behavior).
const (
	orphanHoldTail = 128
	orphanHoldMax  = 32 * 1024
)

// pendingContent is a text/reasoning fragment that arrived while a
// tool_use block was still open. Anthropic blocks must be strictly
// sequential, so the fragment cannot be streamed until the tool block
// closes; flushPendingContent replays it in arrival order right after.
type pendingContent struct {
	kind string // "text" or "reasoning"
	text string
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
	// Free-tier models (notably DeepSeek) echo agentic scaffolding
	// (<system-reminder>, <feature-flag>, DSML tags) into text; strip it
	// so Claude Code never displays the leak.
	if !state.finishSent {
		if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
			// Drain held text first (recovery scans text only, never
			// reasoning: an orphan invoke drafted in thinking is not a
			// tool call, per the official fix).
			events = append(events, flushHold(state, true, false)...)
			if cleaned := SanitizeAssistantText(rc); cleaned != "" {
				events = append(events, handleReasoningContent(cleaned, state)...)
			}
		} else if r, ok := delta["reasoning"].(string); ok && r != "" {
			events = append(events, flushHold(state, true, false)...)
			if cleaned := SanitizeAssistantText(r); cleaned != "" {
				events = append(events, handleReasoningContent(cleaned, state)...)
			}
		}

		// Handle text content via the orphan-invoke hold-back: complete
		// blocks split across deltas become tool_use events, the rest
		// streams through. Sanitizing happens at flush time.
		if txt, ok := delta["content"].(string); ok && txt != "" {
			events = append(events, holdTextContent(txt, state)...)
		}

		// Handle tool calls. Native tool_calls win over recovered ones:
		// held text flushes as plain prose, never double-called.
		if tcList, ok := delta["tool_calls"].([]any); ok && len(tcList) > 0 {
			events = append(events, flushHold(state, false, false)...)
			events = append(events, handleToolCalls(tcList, state)...)
		}
	}

	// Handle usage
	if usage, ok := chunk["usage"].(map[string]any); ok {
		state.usage = extractUsage(usage)
	}

	// Handle finish_reason. A recovered orphan tool call already set
	// tool_calls: an upstream "stop" must not downgrade it, or the
	// client would see tool_use blocks with an end_turn stop reason.
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" && fr != "null" && !state.finishSent {
		if state.finishReason != "tool_calls" {
			state.finishReason = fr
		}
		events = append(events, handleFinish(state)...)
		state.finishSent = true
		state.closed = true
	}

	return events
}

// --- Event generators ---

// closeOpenToolBlocks flushes the buffered (repaired) arguments as a single
// input_json_delta for each open tool_use block, then emits its
// content_block_stop, and resets the tool-call tracking so a later tool call
// starts fresh. Anthropic requires content blocks to be strictly sequential,
// so before opening a new block any open tool_use block must be closed —
// flushing here guarantees the client never receives a tool_use with empty
// input (which Claude Code rejects with "The required parameter `command`
// is missing").
func (s *StreamState) closeOpenToolBlocks() []string {
	var events []string
	for _, blockIdx := range s.openToolBlocks {
		events = append(events, s.flushToolBlockArgs(blockIdx)...)
		events = append(events, contentBlockStop(blockIdx)...)
	}
	s.openToolBlocks = nil
	s.toolCalls = make(map[int]*toolCallInfo)
	s.toolArgBufs = make(map[int]*bytes.Buffer)
	return events
}

// flushToolBlockArgs repairs the arguments accumulated for the tool call
// rendering into the given block index and returns them as one
// input_json_delta event. Returns nil when nothing was buffered; clients
// treat absent deltas as empty input.
func (s *StreamState) flushToolBlockArgs(blockIdx int) []string {
	for intIdx, ti := range s.toolCalls {
		if ti.Index != blockIdx {
			continue
		}
		buf := s.toolArgBufs[intIdx]
		if buf == nil || buf.Len() == 0 {
			return nil
		}
		// Sanitize after repair: a serving stack without the
		// vllm#56302 fix flattens swallowed DSML parameters into the
		// arguments string; truncate values at the first sigil so no
		// markup reaches the tool.
		repaired := SanitizeToolArgs(repairToolArgs(buf.String()))
		buf.Reset()
		return formatSSE("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": blockIdx,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": repaired,
			},
		})
	}
	return nil
}

// flushPendingContent replays content fragments that were pended while a
// tool_use block was open. Must only run once no tool blocks remain open;
// each fragment re-enters its normal handler, which opens and closes its
// own block.
func (s *StreamState) flushPendingContent() []string {
	if len(s.pendingContent) == 0 {
		return nil
	}
	pending := s.pendingContent
	s.pendingContent = nil
	var events []string
	for _, p := range pending {
		switch p.kind {
		case "text":
			events = append(events, handleTextContent(p.text, s)...)
		case "reasoning":
			events = append(events, handleReasoningContent(p.text, s)...)
		}
	}
	return events
}

// closeThinkingBlock closes the currently-open thinking block. Anthropic
// sends a signature_delta event just before content_block_stop on a
// thinking block, used to verify the block's integrity on replay. OpenAI
// upstreams never provide a real signature, so a placeholder is
// synthesized: freegate's own request-side translation (see
// convertClaudeAssistantMessage in request.go) doesn't validate it either,
// but emitting a non-empty signature keeps the event shape compatible with
// clients that expect the field to be present.
func (s *StreamState) closeThinkingBlock() []string {
	var events []string
	events = append(events, formatSSE("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": s.thinkingIdx,
		"delta": map[string]any{
			"type":      "signature_delta",
			"signature": "unsigned",
		},
	})...)
	events = append(events, contentBlockStop(s.thinkingIdx)...)
	s.thinkingOpen = false
	return events
}

func handleReasoningContent(text string, state *StreamState) []string {
	var events []string

	// Reasoning arriving while a tool_use block is open must not force
	// the block closed: closing mid-call discards the still-buffered
	// arguments (Claude Code then rejects the call with "The required
	// parameter `command` is missing") and drops every later fragment.
	// Pend it; flushPendingContent replays it after the block closes.
	if len(state.openToolBlocks) > 0 {
		state.pendingContent = append(state.pendingContent, pendingContent{kind: "reasoning", text: text})
		state.outputContent.WriteString(text)
		return nil
	}

	// Close any open text block first
	if state.textOpen {
		events = append(events, contentBlockStop(state.textBlockIdx)...)
		state.textOpen = false
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
				"type":      "thinking",
				"thinking":  "",
				"signature": "",
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

	// Text arriving while a tool_use block is open must not force the
	// block closed: closing mid-call discards the still-buffered
	// arguments (Claude Code then rejects the call with "The required
	// parameter `command` is missing") and drops every later fragment.
	// Pend it; flushPendingContent replays it after the block closes.
	if len(state.openToolBlocks) > 0 {
		state.pendingContent = append(state.pendingContent, pendingContent{kind: "text", text: text})
		state.outputContent.WriteString(text)
		return nil
	}

	// Close any open thinking block
	if state.thinkingOpen {
		events = append(events, state.closeThinkingBlock()...)
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

// holdTextContent buffers text for orphan-invoke recovery (vllm#48931).
// Complete invoke blocks are extracted into tool_use events; everything
// else streams through, retaining only the trailing window (a split
// marker always lands inside it). Native tool activity bypasses the hold
// entirely: native tool_calls always win over recovered ones.
func holdTextContent(text string, state *StreamState) []string {
	if state.nativeToolSeen {
		return handleTextContent(text, state)
	}
	state.holdBuf.WriteString(text)
	var events []string
	for {
		prose, calls, rest := ExtractOrphanInvokes(state.holdBuf.String())
		if len(calls) == 0 {
			break
		}
		// Prose before the first recovered call streams; prose between a
		// recovered call and the next block is trailing and dropped.
		if prose != "" && !state.orphanRecovered {
			events = append(events, forwardHeldProse(prose, state)...)
		}
		for _, call := range calls {
			events = append(events, emitOrphanToolUse(call, state)...)
		}
		state.holdBuf.Reset()
		state.holdBuf.WriteString(rest)
	}
	buf := state.holdBuf.String()
	// Trailing prose after a recovered call is dropped — unless it could
	// still grow into an invoke opener (partial marker). firstPlausible
	// matching (not whole-word) is essential here: a partial opener like
	// "<｜DSML" must survive until it resolves.
	if i := firstPlausibleOpener(buf); i < 0 {
		state.holdBuf.Reset()
		if buf != "" && !state.orphanRecovered {
			events = append(events, forwardHeldProse(buf, state)...)
		}
		return events
	} else {
		// A possible opener is forming: flush the settled head, retain
		// from its '<' so a split marker resolves once complete. Give up
		// past orphanHoldMax (flush as text, today's behavior). After a
		// recovered call the head is trailing and dropped instead.
		if i > 0 || len(buf) > orphanHoldMax {
			head := buf[:max(i, len(buf)-orphanHoldTail)]
			state.holdBuf.Reset()
			state.holdBuf.WriteString(buf[len(head):])
			if !state.orphanRecovered {
				events = append(events, forwardHeldProse(head, state)...)
			}
		}
	}
	return events
}

// firstPlausibleOpener returns the index of the earliest '<' whose suffix
// could still grow into a DSML invoke-family opener, or -1 when no such
// suffix exists. Retaining from the earliest (not latest) plausible '<'
// keeps a partially arrived opener intact while settled prose flushes.
func firstPlausibleOpener(buf string) int {
	for i := 0; i < len(buf); i++ {
		if buf[i] != '<' {
			continue
		}
		if couldBeInvokeOpener(buf[i:]) {
			return i
		}
	}
	return -1
}

// couldBeInvokeOpener reports whether frag (text from a '<' to end of
// buffer) could still grow into a DSML invoke-family opener as more
// deltas arrive. Close tags never open. A tag region terminated by '>'
// or '/' is complete and holds only on exact keyword match; an
// unterminated region holds on prefix match so a split marker resolves
// once its bytes arrive. Anything else flushes immediately.
func couldBeInvokeOpener(frag string) bool {
	s := frag[1:] // drop '<'
	s = strings.TrimLeft(s, " \t\n\r")
	if strings.HasPrefix(s, "/") {
		return false // close tags never open
	}
	// Tag-name region ends at the first whitespace, '>', or '/'.
	end := len(s)
	if i := strings.IndexAny(s, " \t\n\r>/"); i >= 0 {
		end = i
	}
	completed := end < len(s) && (s[end] == '>' || s[end] == '/')
	tok := strings.ToLower(s[:end])
	stripSigil := func(t string) string {
		t = strings.TrimPrefix(t, "｜dsml｜")
		t = strings.TrimPrefix(t, "|dsml|")
		return t
	}
	if completed {
		switch stripSigil(tok) {
		case "invoke", "parameter", "tool_calls", "tool-calls", "dsml":
			return true
		}
		return tok == "｜dsml｜" || tok == "|dsml|" || tok == "｜" || tok == "|"
	}
	norm := stripSigil(tok)
	for _, kw := range []string{
		"｜dsml｜invoke", "|dsml|invoke", "invoke",
		"｜dsml｜parameter", "parameter",
		"｜dsml｜tool_calls", "|dsml|tool_calls", "tool_calls", "tool-calls",
		"｜dsml｜", "|dsml|", "｜", "|", "dsml",
	} {
		if strings.HasPrefix(kw, norm) || strings.HasPrefix(norm, kw) {
			return true
		}
	}
	return false
}

// forwardHeldProse sanitizes and forwards buffered prose as text.
func forwardHeldProse(prose string, state *StreamState) []string {
	if cleaned := SanitizeAssistantText(prose); cleaned != "" {
		return handleTextContent(cleaned, state)
	}
	return nil
}

// emitOrphanToolUse opens a tool_use block for a recovered call and
// registers it like a native one, so handleFinish flushes its arguments
// and closes the block. The turn ended in a tool call, so the finish
// reason becomes tool_calls.
func emitOrphanToolUse(call OrphanToolCall, state *StreamState) []string {
	var events []string
	if state.textOpen {
		events = append(events, contentBlockStop(state.textBlockIdx)...)
		state.textOpen = false
	}
	if state.thinkingOpen {
		events = append(events, state.closeThinkingBlock()...)
	}
	id := "toolu_" + randID(8)
	blockIdx := state.nextBlockIdx
	state.nextBlockIdx++
	state.orphanRecovered = true
	// Negative keys cannot collide with upstream tool indexes (>= 0).
	key := -(blockIdx + 1)
	state.toolCalls[key] = &toolCallInfo{ID: id, Name: call.Name, Index: blockIdx}
	argBuf := &bytes.Buffer{}
	argBuf.WriteString(call.Input)
	state.toolArgBufs[key] = argBuf
	state.openToolBlocks = append(state.openToolBlocks, blockIdx)
	state.outputContent.WriteString(call.Input)
	events = append(events, formatSSE("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": blockIdx,
		"content_block": map[string]any{
			"type": "tool_use",
			"id":   id,
			"name": call.Name,
		},
	})...)
	if state.finishReason == "" || state.finishReason == "stop" {
		state.finishReason = "tool_calls"
	}
	return events
}

// flushHold drains the hold buffer. With extract, complete orphan invokes
// become tool_use events and only the unconsumed tail is retained;
// otherwise (native tool_calls exist) everything flushes as plain text.
// Callers with terminal semantics (stream finish) pass dropTrailing to
// drop prose after the last recovered call, mirroring the official fix.
func flushHold(state *StreamState, extract bool, dropTrailing bool) []string {
	if state.holdBuf.Len() == 0 {
		return nil
	}
	buf := state.holdBuf.String()
	state.holdBuf.Reset()
	if !extract {
		return forwardHeldProse(buf, state)
	}
	prose, calls, rest := ExtractOrphanInvokes(buf)
	if len(calls) == 0 {
		// firstPlausible (not whole-word) matching: a partial opener must
		// survive; only provably-dead trailing prose is dropped.
		if dropTrailing && state.orphanRecovered && firstPlausibleOpener(buf) < 0 {
			// Trailing prose after a recovered call is dropped: a tool
			// call ends the turn (official semantics). An in-progress
			// block still forwards as text (today's behavior).
			return nil
		}
		return forwardHeldProse(buf, state)
	}
	var events []string
	// Prose before the first recovered call streams; prose after one is
	// trailing and dropped once orphanRecovered is set (see below).
	if prose != "" && !state.orphanRecovered {
		events = append(events, forwardHeldProse(prose, state)...)
	}
	for _, call := range calls {
		events = append(events, emitOrphanToolUse(call, state)...)
	}
	if rest != "" {
		if dropTrailing {
			// Trailing prose after a tool call is dropped (official). An
			// in-progress block still forwards as text (today's behavior).
			if HasInvokeOpener(rest) {
				events = append(events, forwardHeldProse(rest, state)...)
			}
		} else {
			// Mid-stream: retain for completion by later deltas.
			state.holdBuf.WriteString(rest)
		}
	}
	return events
}

// FlushHoldAtEnd drains the hold buffer when the upstream stream ends
// without a finish_reason chunk (handleFinish never runs there).
func (s *StreamState) FlushHoldAtEnd() []string {
	return flushHold(s, !s.nativeToolSeen, true)
}

func handleToolCalls(tcList []any, state *StreamState) []string {
	var events []string
	state.nativeToolSeen = true

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
					events = append(events, state.closeThinkingBlock()...)
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
	// Drop consecutive identical parts: hy3 often emits the same tool call
	// twice (e.g. {"command":"ls"}{"command":"ls"}). Running a duplicate
	// would re-execute the command needlessly.
	deduped := parts[:0]
	for _, p := range parts {
		if len(deduped) > 0 && deduped[len(deduped)-1] == p {
			continue
		}
		deduped = append(deduped, p)
	}
	return deduped
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
// RepairToolArgs is the exported entry point used by the OpenAI→OpenAI
// response normalizer (normalize.go) to salvage malformed tool-call
// arguments. It always returns a valid JSON object (or "{}").
func RepairToolArgs(s string) string {
	return repairToolArgs(s)
}

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

	// Drain the hold buffer first: recovered calls must open their blocks
	// before anything closes. Trailing prose after a recovered call is
	// dropped (a tool call ends the turn, per the official fix).
	events = append(events, flushHold(state, !state.nativeToolSeen, true)...)

	// Close open blocks
	if state.textOpen {
		events = append(events, contentBlockStop(state.textBlockIdx)...)
		state.textOpen = false
	}
	if state.thinkingOpen {
		events = append(events, state.closeThinkingBlock()...)
	}

	// Close any open tool_use blocks; each receives its repaired
	// arguments as a single input_json_delta immediately before its
	// content_block_stop.
	events = append(events, state.closeOpenToolBlocks()...)

	// Replay any text/reasoning that was pended behind the tool blocks so
	// it still reaches the client before the message ends.
	events = append(events, state.flushPendingContent()...)

	// The replay may have opened new text/thinking blocks; close them so
	// every block is terminated before message_delta/message_stop.
	if state.textOpen {
		events = append(events, contentBlockStop(state.textBlockIdx)...)
		state.textOpen = false
	}
	if state.thinkingOpen {
		events = append(events, state.closeThinkingBlock()...)
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
