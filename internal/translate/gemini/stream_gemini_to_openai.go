package gemini

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// GeminiToOpenAIState tracks per-stream state for the Gemini → OpenAI
// streaming translation. Used when the upstream speaks native Gemini
// SSE (newline-delimited JSON objects) and the client expects OpenAI
// chat-completion chunks.
//
// Mirrors the state model of 9router's response/gemini-to-openai.js
// (minus the unsupported envelope / masking logic specific to that
// deployment).
type GeminiToOpenAIState struct {
	messageID     string
	model         string
	created       int64
	toolCallIndex int
	toolCalls     map[int]*g2oToolCall // key: part index in the response
	textStarted   bool
	usage         *g2oUsage
	finishReason  string
	finishSent    bool
	closed        bool
	sseBuf        bytes.Buffer
}

type g2oToolCall struct {
	Index int    // OpenAI tool_call.index
	ID    string // OpenAI tool_call.id
	Name  string // OpenAI tool_call.function.name
}

type g2oUsage struct {
	PromptTokens     int64
	CandidatesTokens int64
	ThoughtsTokens   int64
	CachedTokens     int64
}

// NewGeminiToOpenAIState constructs a new state with a random message id
// and a creation timestamp.
func NewGeminiToOpenAIState() *GeminiToOpenAIState {
	return &GeminiToOpenAIState{
		messageID: randomID(12),
		created:   time.Now().Unix(),
		toolCalls: make(map[int]*g2oToolCall),
	}
}

// Feed appends incoming bytes and returns complete newline-terminated
// lines. Partial trailing data is retained for the next call.
func (s *GeminiToOpenAIState) Feed(p []byte) []string {
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

// IsClosed reports whether the stream has finished.
func (s *GeminiToOpenAIState) IsClosed() bool { return s.closed }

// ProcessChunk converts one parsed Gemini chunk into the OpenAI
// chat.completion.chunk SSE line(s) to emit. Returns nil if nothing
// should be emitted for this chunk.
//
// Each emitted line is a complete SSE record: "data: <json>\n\n".
func (s *GeminiToOpenAIState) ProcessChunk(chunk map[string]any) []string {
	if s.closed {
		return nil
	}

	if model, ok := chunk["modelVersion"].(string); ok && model != "" {
		s.model = model
	}

	// Usage metadata
	if um, ok := chunk["usageMetadata"].(map[string]any); ok {
		s.usage = parseGeminiUsage(um)
	}

	cands, _ := chunk["candidates"].([]any)
	if len(cands) == 0 {
		// Usage-only chunk with no candidates — could be a trailing
		// usage chunk. Emit it as a final usage chunk if we have not
		// already sent the finish chunk.
		if s.usage != nil && !s.finishSent {
			// Do not auto-finalize: the server typically sends the
			// finish chunk *after* the usage. Just remember the usage.
		}
		return nil
	}
	c0, _ := cands[0].(map[string]any)
	if c0 == nil {
		return nil
	}

	content, _ := c0["content"].(map[string]any)
	parts, _ := content["parts"].([]any)

	// First emission: send the role prelude chunk.
	if !s.textStarted && len(parts) > 0 {
		s.textStarted = true
	}

	var results []string

	// If the upstream included a finish reason, capture it.
	if fr, ok := c0["finishReason"].(string); ok && fr != "" {
		s.finishReason = mapFinishReasonOpenAI(fr)
	}

	// Walk parts. For text parts: emit content delta. For thought
	// parts (Gemini thinking): emit reasoning_content. For
	// functionCall parts: emit a single tool_call delta (Gemini does
	// not stream args incrementally for tool calls).
	for i, pAny := range parts {
		part, ok := pAny.(map[string]any)
		if !ok {
			continue
		}

		if t, ok := part["text"].(string); ok {
			isThought, _ := part["thought"].(bool)
			if isThought {
				if t == "" {
					continue
				}
				results = append(results, s.chunkLine(map[string]any{"reasoning_content": t}, nil))
			} else {
				if t == "" {
					continue
				}
				results = append(results, s.chunkLine(map[string]any{"content": t}, nil))
			}
			continue
		}

		if fc, ok := part["functionCall"].(map[string]any); ok {
			name, _ := fc["name"].(string)
			if name == "" {
				continue
			}
			args, _ := fc["args"].(map[string]any)
			if args == nil {
				args = map[string]any{}
			}
			argsBytes, _ := json.Marshal(args)
			tc := &g2oToolCall{Index: s.toolCallIndex, ID: "call_gemini_" + name + "_" + randomID(6), Name: name}
			s.toolCalls[i] = tc
			s.toolCallIndex++
			results = append(results, s.chunkLine(map[string]any{
				"tool_calls": []any{map[string]any{
					"index": tc.Index,
					"id":    tc.ID,
					"type":  "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": string(argsBytes),
					},
				}},
			}, nil))
		}
	}

	// If the candidate carried a finish reason, emit the final chunk.
	if s.finishReason != "" && !s.finishSent {
		results = append(results, s.finalChunk())
		s.finishSent = true
		s.closed = true
	}

	return results
}

func (s *GeminiToOpenAIState) chunkLine(delta map[string]any, finishReason *string) string {
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

func (s *GeminiToOpenAIState) finalChunk() string {
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
		completion := s.usage.CandidatesTokens + s.usage.ThoughtsTokens
		u := map[string]any{
			"prompt_tokens":     s.usage.PromptTokens,
			"completion_tokens": completion,
			"total_tokens":      s.usage.PromptTokens + completion,
		}
		if s.usage.CachedTokens > 0 {
			u["prompt_tokens_details"] = map[string]any{
				"cached_tokens": s.usage.CachedTokens,
			}
		}
		if s.usage.ThoughtsTokens > 0 {
			u["completion_tokens_details"] = map[string]any{
				"reasoning_tokens": s.usage.ThoughtsTokens,
			}
		}
		chunk["usage"] = u
	}
	data, err := json.Marshal(chunk)
	if err != nil {
		return ""
	}
	return "data: " + string(data) + "\n\n"
}

// parseGeminiUsage converts a Gemini usageMetadata object into the
// internal g2oUsage form.
func parseGeminiUsage(um map[string]any) *g2oUsage {
	out := &g2oUsage{}
	if v, ok := asInt64(um["promptTokenCount"]); ok {
		out.PromptTokens = v
	}
	if v, ok := asInt64(um["candidatesTokenCount"]); ok {
		out.CandidatesTokens = v
	}
	if v, ok := asInt64(um["thoughtsTokenCount"]); ok {
		out.ThoughtsTokens = v
	}
	if v, ok := asInt64(um["cachedContentTokenCount"]); ok {
		out.CachedTokens = v
	}
	return out
}
