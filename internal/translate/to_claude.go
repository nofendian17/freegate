package translate

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
)

// --- Public types ---

// ResponseWriter wraps an http.ResponseWriter to translate upstream responses
// from OpenAI format back to the source (Claude/Gemini) format.
// It intercepts Write calls and performs format-specific translation
// for both streaming (SSE) and non-streaming (JSON) responses.
type ResponseWriter struct {
	inner      http.ResponseWriter
	format     Format
	isStream   bool
	statusCode int
	buf        bytes.Buffer // for non-streaming buffering
	state      any          // streaming state machine (type-specific)
	headerWritten bool
}

// NewResponseWriter creates a response translator for the given source format.
func NewResponseWriter(w http.ResponseWriter, format Format) *ResponseWriter {
	return &ResponseWriter{
		inner:  w,
		format: format,
	}
}

func (rw *ResponseWriter) Header() http.Header {
	return rw.inner.Header()
}

func (rw *ResponseWriter) WriteHeader(statusCode int) {
	rw.statusCode = statusCode
	rw.headerWritten = true
	// Only pass through non-200 headers immediately; for 200 we may
	// modify Content-Type and delay headers until first write.
	if statusCode != http.StatusOK {
		copyHeaders(rw.inner, rw.inner.Header())
		rw.inner.WriteHeader(statusCode)
	}
}

func (rw *ResponseWriter) Write(p []byte) (int, error) {
	if rw.statusCode == 0 {
		rw.statusCode = http.StatusOK
	}

	// Error responses pass through untranslated
	if rw.statusCode != http.StatusOK {
		if !rw.headerWritten {
			rw.inner.WriteHeader(rw.statusCode)
		}
		return rw.inner.Write(p)
	}

	// Determine streaming on first write
	if !rw.isStream {
		ct := rw.Header().Get("Content-Type")
		rw.isStream = strings.Contains(ct, "text/event-stream")
	}

	if rw.isStream {
		return rw.writeStream(p)
	}

	// Non-streaming: buffer for later translation
	n, _ := rw.buf.Write(p)
	return n, nil
}

// Close must be called to flush any buffered non-streaming response.
func (rw *ResponseWriter) Close() error {
	if rw.statusCode != http.StatusOK {
		return nil
	}
	if rw.isStream {
		return nil
	}
	if rw.buf.Len() == 0 {
		return nil
	}

	translated, err := translateJSONResponse(rw.buf.Bytes(), rw.format)
	if err != nil {
		slog.Warn("translate: json response translation failed, passing through", "error", err)
		translated = rw.buf.Bytes()
	}
	if !rw.headerWritten {
		rw.inner.WriteHeader(http.StatusOK)
	}
	_, err = rw.inner.Write(translated)
	return err
}

// --- Streaming: SSE line buffering ---

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

// --- Streaming: OpenAI → Claude state machine ---
type claudeStreamState struct {
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
}

// drainLines extracts complete lines separated by \n from the sseBuf.
// Retains partial trailing data in the buffer for the next call.
func (s *claudeStreamState) drainLines() []string {
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

func newClaudeStream() *claudeStreamState {
	return &claudeStreamState{
		messageID:   "msg_" + randID(8),
		nextBlockIdx: 0,
		toolCalls:   make(map[int]*toolCallInfo),
		toolArgBufs:  make(map[int]*bytes.Buffer),
	}
}

// writeStream translates OpenAI SSE bytes → Claude SSE events and writes to inner.
// Each Write() call receives one or more SSE lines (separated by \n).
func (rw *ResponseWriter) writeStream(p []byte) (int, error) {
	if rw.state == nil {
		rw.state = newClaudeStream()
	}
	state := rw.state.(*claudeStreamState)
	if state.closed {
		return len(p), nil
	}

	// Accumulate incoming bytes and extract complete lines.
	state.sseBuf.Write(p)
	lines := state.drainLines()

	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			// Non-data lines (event:, empty lines, etc.) pass through
			rw.writeLine(line + "\n")
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		data = strings.TrimRight(data, "\r\n ")

		if data == "[DONE]" {
			state.closed = true
			continue
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			rw.writeLine(line + "\n")
			continue
		}

		events := processOpenAIChunk(chunk, state)
		for _, evt := range events {
			rw.writeLine(evt)
		}
	}
	return len(p), nil
}

func (rw *ResponseWriter) processBlocks(blocks []string, state *claudeStreamState, orig []byte) (int, error) {
	for _, block := range blocks {
		// Split into individual lines
		lines := strings.Split(block, "\n")
		for _, line := range lines {
			if !strings.HasPrefix(line, "data: ") {
				// Non-data lines (event:, etc.) pass through
				rw.writeLine(line + "\n")
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			data = strings.TrimRight(data, "\r\n ")

			if data == "[DONE]" {
				state.closed = true
				continue
			}

			var chunk map[string]any
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				// Unparseable data → pass through
				rw.writeLine(line + "\n")
				continue
			}

			events := processOpenAIChunk(chunk, state)
			for _, evt := range events {
				rw.writeLine(evt)
			}
		}
	}
	return len(orig), nil
}

func (rw *ResponseWriter) writeLine(s string) {
	if !rw.headerWritten {
		rw.inner.Header().Set("Content-Type", "text/event-stream")
		rw.inner.Header().Set("Cache-Control", "no-cache")
		rw.inner.Header().Set("Connection", "keep-alive")
		rw.inner.WriteHeader(http.StatusOK)
		rw.headerWritten = true
	}
	io.WriteString(rw.inner, s)
	if fl, ok := rw.inner.(http.Flusher); ok {
		fl.Flush()
	}
}

// --- Core chunk processing ---

func processOpenAIChunk(chunk map[string]any, state *claudeStreamState) []string {
	var events []string

	// Extract choices
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		// Might be a usage-only chunk or error
		if usage, ok := chunk["usage"].(map[string]any); ok && !state.messageStartSent {
			// Usage in first chunk
			state.usage = extractUsage(usage)
			emitMessageStart(state)
			events = append(events, formatSSE("message_start", map[string]any{
				"type":    "message_start",
				"message": buildClaudeMessage(state),
			})...)
			state.messageStartSent = true
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

	// Emit message_start on first relevant delta
	if !state.messageStartSent {
		events = append(events, formatSSE("message_start", map[string]any{
			"type":    "message_start",
			"message": buildClaudeMessage(state),
		})...)
		state.messageStartSent = true
	}

	// Handle reasoning_content (Claude thinking)
	if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
		events = append(events, handleReasoningContent(rc, state)...)
	}
	// Also check for "reasoning" field
	if r, ok := delta["reasoning"].(string); ok && r != "" {
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

	// Handle usage
	if usage, ok := chunk["usage"].(map[string]any); ok {
		state.usage = extractUsage(usage)
	}

	// Handle finish_reason
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" && fr != "null" && !state.finishSent {
		state.finishReason = fr
		events = append(events, handleFinish(state)...)
		state.finishSent = true
	}

	return events
}

// --- Event generators ---

func handleReasoningContent(text string, state *claudeStreamState) []string {
	var events []string

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
				"type": "thinking",
			},
		})...)
	}

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

func handleTextContent(text string, state *claudeStreamState) []string {
	var events []string

	// Close any open thinking block
	if state.thinkingOpen {
		events = append(events, contentBlockStop(state.thinkingIdx)...)
		state.thinkingOpen = false
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

func handleToolCalls(tcList []any, state *claudeStreamState) []string {
	var events []string

	for _, tcAny := range tcList {
		tc, ok := tcAny.(map[string]any)
		if !ok {
			continue
		}

		idx, _ := tc["index"].(float64)
		intIdx := int(idx)

		if id, ok := tc["id"].(string); ok && id != "" {
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

		// Accumulate arguments
		if fn, ok := tc["function"].(map[string]any); ok {
			if args, ok := fn["arguments"].(string); ok && args != "" {
				state.toolArgBufs[intIdx].WriteString(args)
				ti := state.toolCalls[intIdx]
				if ti != nil {
					events = append(events, formatSSE("content_block_delta", map[string]any{
						"type":  "content_block_delta",
						"index": ti.Index,
						"delta": map[string]any{
							"type":         "input_json_delta",
							"partial_json": args,
						},
					})...)
				}
			}
		}
	}

	return events
}

func handleFinish(state *claudeStreamState) []string {
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

	// Close any open tool_use blocks
	for _, blockIdx := range state.openToolBlocks {
		events = append(events, contentBlockStop(blockIdx)...)
	}
	state.openToolBlocks = nil

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

// --- Helpers ---

func buildClaudeMessage(state *claudeStreamState) map[string]any {
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
		msg["usage"] = map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
		}
	}
	return msg
}

func extractUsage(u map[string]any) *usageInfo {
	ui := &usageInfo{}
	if pt, ok := u["prompt_tokens"].(float64); ok {
		ui.InputTokens = int64(pt)
	}
	if ct, ok := u["completion_tokens"].(float64); ok {
		ui.OutputTokens = int64(ct)
	}
	// Check for cache tokens in prompt_tokens_details
	if details, ok := u["prompt_tokens_details"].(map[string]any); ok {
		if cached, ok := details["cached_tokens"].(float64); ok {
			ui.CacheReadTokens = int64(cached)
			// Subtract from input_tokens to match Claude's accounting
			ui.InputTokens -= ui.CacheReadTokens
		}
		if cc, ok := details["cache_creation_tokens"].(float64); ok {
			ui.CacheCreateTokens = int64(cc)
		}
	}
	return ui
}

func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "end_turn"
	default:
		return "end_turn"
	}
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

func emitMessageStart(state *claudeStreamState) {
	state.messageStartSent = true
}

// --- Non-streaming JSON response translation ---

func translateJSONResponse(body []byte, format Format) ([]byte, error) {
	switch format {
	case FormatClaude:
		return openaiJSONToClaude(body)
	case FormatGemini:
		return openaiJSONToGemini(body)
	default:
		return body, nil
	}
}

func openaiJSONToClaude(body []byte) ([]byte, error) {
	var openaiResp map[string]any
	if err := json.Unmarshal(body, &openaiResp); err != nil {
		return nil, err
	}

	claude := map[string]any{
		"id":         "msg_" + randID(8),
		"type":       "message",
		"role":       "assistant",
		"content":    []any{},
		"model":      openaiResp["model"],
		"stop_reason":   nil,
		"stop_sequence": nil,
	}

	// Extract usage
	if usage, ok := openaiResp["usage"].(map[string]any); ok {
		claude["usage"] = extractUsage(usage)
	} else {
		claude["usage"] = map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
		}
	}

	// Convert choices to content blocks
	if choices, ok := openaiResp["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		if choice != nil {
			stopReason := ""
			if fr, ok := choice["finish_reason"].(string); ok {
				stopReason = mapFinishReason(fr)
			}
			claude["stop_reason"] = stopReason

			msg, _ := choice["message"].(map[string]any)
			if msg != nil {
				content := convertOpenAIMessage(msg)
				claude["content"] = content
			}
		}
	}

	return json.Marshal(claude)
}

func convertOpenAIMessage(msg map[string]any) []any {
	var content []any

	// Add text content
	switch c := msg["content"].(type) {
	case string:
		if c != "" {
			content = append(content, map[string]any{
				"type": "text",
				"text": c,
			})
		}
	case []any:
		for _, part := range c {
			content = append(content, part)
		}
	}

	// Add tool calls as tool_use blocks
	if tcList, ok := msg["tool_calls"].([]any); ok {
		for _, tcAny := range tcList {
			tc, _ := tcAny.(map[string]any)
			if tc == nil {
				continue
			}
			fn, _ := tc["function"].(map[string]any)
			if fn == nil {
				continue
			}
			name, _ := fn["name"].(string)
			argsStr, _ := fn["arguments"].(string)
			var input any
			json.Unmarshal([]byte(argsStr), &input)
			if input == nil {
				input = map[string]any{}
			}

			id, _ := tc["id"].(string)
			if id == "" {
				id = "toolu_" + randID(8)
			}

			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  name,
				"input": input,
			})
		}
	}

	if len(content) == 0 {
		content = append(content, map[string]any{
			"type": "text",
			"text": "",
		})
	}

	return content
}

func openaiJSONToGemini(body []byte) ([]byte, error) {
	var openaiResp map[string]any
	if err := json.Unmarshal(body, &openaiResp); err != nil {
		return nil, err
	}

	gemini := map[string]any{
		"candidates": []any{},
	}

	if choices, ok := openaiResp["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		if choice != nil {
			candidate := map[string]any{
				"index":         0,
				"finishReason":  mapFinishReasonGemini(choice["finish_reason"]),
				"content": map[string]any{
					"parts": []any{},
					"role":  "model",
				},
			}

			if msg, ok := choice["message"].(map[string]any); ok {
				if txt, ok := msg["content"].(string); ok && txt != "" {
					candidate["content"].(map[string]any)["parts"] = []any{
						map[string]any{"text": txt},
					}
				}
			}

			gemini["candidates"] = []any{candidate}
		}
	}

	// Usage metadata
	if usage, ok := openaiResp["usage"].(map[string]any); ok {
		gemini["usageMetadata"] = map[string]any{
			"promptTokenCount":     usage["prompt_tokens"],
			"candidatesTokenCount": usage["completion_tokens"],
		}
	}

	return json.Marshal(gemini)
}

func mapFinishReasonGemini(reason any) string {
	r, _ := reason.(string)
	switch r {
	case "stop":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "BLOCKED"
	default:
		return "STOP"
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

// copyHeaders copies all headers from src to dst, filtering hop-by-hop headers.
func copyHeaders(dst http.ResponseWriter, src http.Header) {
	hopByHop := map[string]bool{
		"Connection":          true,
		"Proxy-Connection":    true,
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"TE":                  true,
		"Trailers":            true,
		"Transfer-Encoding":   true,
		"Upgrade":             true,
	}
	for k, vs := range src {
		if hopByHop[k] {
			continue
		}
		for _, v := range vs {
			dst.Header().Add(k, v)
		}
	}
}
