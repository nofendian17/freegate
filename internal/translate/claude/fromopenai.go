package claude

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ============================================================================
// Input types — OpenAI chat-completions request body shape.
//
// These mirror the subset of OpenAI fields the translator needs to inspect.
// Fields that are pure pass-through (metadata, thinking) are kept as
// json.RawMessage to avoid unmarshal/remarshal cost.
// ============================================================================

type oaiRequest struct {
	Model               string             `json:"model,omitempty"`
	MaxTokens           *int               `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int               `json:"max_completion_tokens,omitempty"`
	Temperature         *float64           `json:"temperature,omitempty"`
	TopP                *float64           `json:"top_p,omitempty"`
	Stream              *bool              `json:"stream,omitempty"`
	Metadata            json.RawMessage    `json:"metadata,omitempty"`
	StopSequences       []string           `json:"stop_sequences,omitempty"`
	Stop                json.RawMessage    `json:"stop,omitempty"`
	TopK                *int               `json:"top_k,omitempty"`
	Messages            []oaiMessage       `json:"messages"`
	Tools               []oaiTool          `json:"tools,omitempty"`
	ToolChoice          json.RawMessage    `json:"tool_choice,omitempty"`
	Thinking            json.RawMessage    `json:"thinking,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"`
	ResponseFormat      *oaiResponseFormat `json:"response_format,omitempty"`
}

type oaiMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"` // string or []oaiContentPart
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  []oaiToolCall   `json:"tool_calls,omitempty"`
}

// oaiContentPart represents one element of a content array. The same
// shape is used for both user content (text, image_url, image,
// tool_result) and assistant content (text, tool_use, thinking);
// unused fields stay at their zero value.
type oaiContentPart struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ImageURL   *oaiImageURL    `json:"image_url,omitempty"`
	Source     json.RawMessage `json:"source,omitempty"`
	ToolUseID  string          `json:"tool_use_id,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	IsError    *bool           `json:"is_error,omitempty"`
	// Content on a tool_result part (string or array of parts).
	Content json.RawMessage `json:"content,omitempty"`
	// Assistant content only:
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
}

type oaiImageURL struct {
	URL string `json:"url"`
}

// oaiTool represents both the OpenAI shape ({type, function}) and the
// Claude-native shape ({name, description, input_schema}) plus
// built-in pass-through tools. Unused fields stay zero.
type oaiTool struct {
	Type        string           `json:"type,omitempty"`
	Function    *oaiToolFunction `json:"function,omitempty"`
	Name        string           `json:"name,omitempty"`
	Description string           `json:"description,omitempty"`
	InputSchema json.RawMessage  `json:"input_schema,omitempty"`
	Parameters  json.RawMessage  `json:"parameters,omitempty"`
}

type oaiToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type oaiToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type,omitempty"`
	Function oaiToolCallFunction `json:"function"`
}

type oaiToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

type oaiResponseFormat struct {
	Type       string         `json:"type"`
	JSONSchema *oaiJSONSchema `json:"json_schema,omitempty"`
}

type oaiJSONSchema struct {
	Schema json.RawMessage `json:"schema,omitempty"`
}

// ============================================================================
// Output types — Claude Messages API request body shape.
// ============================================================================

type claudeRequest struct {
	Model         string              `json:"model,omitempty"`
	MaxTokens     *int                `json:"max_tokens,omitempty"`
	Temperature   *float64            `json:"temperature,omitempty"`
	TopP          *float64            `json:"top_p,omitempty"`
	Stream        *bool               `json:"stream,omitempty"`
	Metadata      json.RawMessage     `json:"metadata,omitempty"`
	StopSequences []string            `json:"stop_sequences,omitempty"`
	TopK          *int                `json:"top_k,omitempty"`
	System        []claudeSystemBlock `json:"system,omitempty"`
	Messages      []claudeMessage     `json:"messages,omitempty"`
	Tools         []claudeTool        `json:"tools,omitempty"`
	ToolChoice    json.RawMessage     `json:"tool_choice,omitempty"`
	Thinking      *claudeThinking     `json:"thinking,omitempty"`
}

type claudeSystemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Content []claudeContent `json:"content"`
}

// claudeContent is the union of all Claude content-block types.
// Unused fields stay at their zero value, so e.g. a text block
// marshals as {"type":"text","text":"..."} only.
type claudeContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Source    json.RawMessage `json:"source,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   *bool           `json:"is_error,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
}

type claudeTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type claudeThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type claudeToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// ============================================================================
// FromOpenAI — entry point
// ============================================================================

// FromOpenAI converts an OpenAI-format chat-completions request body to
// Claude format. Mirrors 9router's request/openai-to-claude.js, minus
// the Claude-OAuth tool-name prefixing and the Claude Code system-prompt
// injection (those are specific to that project's deployment).
//
// The caller is expected to have already run prepost.* helpers on the
// OpenAI body (AdjustMaxTokens, EnsureToolCallIds, FixMissingToolResponses,
// NormalizeThinkingConfig) so the input is already normalized.
func FromOpenAI(body []byte) ([]byte, error) {
	var src oaiRequest
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("claude: invalid FromOpenAI body: %w", err)
	}

	out := claudeRequest{
		Model:         src.Model,
		MaxTokens:     src.MaxTokens,
		Temperature:   src.Temperature,
		TopP:          src.TopP,
		Stream:        src.Stream,
		Metadata:      src.Metadata,
		StopSequences: mergeStop(src.StopSequences, src.Stop),
		TopK:          src.TopK,
	}
	// OpenAI's newer `max_completion_tokens` (o1-era) supersedes
	// `max_tokens`; apply after the max_tokens pass-through so it wins.
	if src.MaxCompletionTokens != nil {
		out.MaxTokens = src.MaxCompletionTokens
	}

	// System prompt: from messages[role=system] + response_format.
	systemParts := collectSystemParts(src)
	if src.ResponseFormat != nil {
		if extra := convertResponseFormatToSystem(*src.ResponseFormat); extra != "" {
			systemParts = append(systemParts, extra)
		}
	}
	if len(systemParts) > 0 {
		blocks := make([]claudeSystemBlock, 0, len(systemParts))
		for _, s := range systemParts {
			blocks = append(blocks, claudeSystemBlock{Type: "text", Text: s})
		}
		out.System = blocks
	}

	// Build messages by walking non-system messages with a state machine
	// that merges consecutive same-role entries and flushes after each
	// tool_use (Claude requires tool_use in its own assistant message).
	msgs, err := buildClaudeMessages(src)
	if err != nil {
		return nil, err
	}
	out.Messages = msgs

	// Tools. OpenAI's tool_choice="none" means "do not call any tool" —
	// omit the tools block in that case so Claude has no tools to choose
	// from. Claude's "auto" = model decides, which would otherwise
	// override the caller's intent.
	if !isToolChoiceNone(src.ToolChoice) {
		if len(src.Tools) > 0 {
			claudeTools := make([]claudeTool, 0, len(src.Tools))
			for _, t := range src.Tools {
				if ct, ok := convertOaiTool(t); ok {
					claudeTools = append(claudeTools, ct)
				}
			}
			if len(claudeTools) > 0 {
				out.Tools = claudeTools
			}
		}
	}

	// Tool choice
	if len(src.ToolChoice) > 0 {
		out.ToolChoice = convertOpenAIToolChoice(src.ToolChoice)
	}

	// Thinking: pass-through, else map reasoning_effort
	if len(src.Thinking) > 0 {
		var th claudeThinking
		if err := json.Unmarshal(src.Thinking, &th); err == nil {
			out.Thinking = &th
		}
	} else if eff := src.ReasoningEffort; eff != "" {
		if budget := reasoningEffortToBudget(eff); budget > 0 {
			out.Thinking = &claudeThinking{Type: "enabled", BudgetTokens: budget}
		}
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("claude: marshal FromOpenAI: %w", err)
	}
	return encoded, nil
}

// ============================================================================
// Message walker
// ============================================================================

func buildClaudeMessages(src oaiRequest) ([]claudeMessage, error) {
	if len(src.Messages) == 0 {
		return nil, nil
	}

	out := make([]claudeMessage, 0, len(src.Messages))
	var currentRole string
	var currentBlocks []claudeContent

	flush := func() {
		if currentRole != "" && len(currentBlocks) > 0 {
			out = append(out, claudeMessage{
				Role:    currentRole,
				Content: currentBlocks,
			})
		}
		currentRole = ""
		currentBlocks = nil
	}

	for _, m := range src.Messages {
		if m.Role == "system" {
			continue
		}
		// "tool" and "user" messages become Claude "user" messages;
		// "assistant" stays "assistant" (the zero value, no rewrite
		// needed). Unknown roles are rejected upstream by
		// openaiMessageToBlocks, so no default branch is necessary.
		newRole := m.Role
		if m.Role == "tool" || m.Role == "user" {
			newRole = "user"
		}

		blocks, err := openaiMessageToBlocks(m)
		if err != nil {
			return nil, err
		}
		hasToolResult := false
		hasToolUse := false
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				hasToolResult = true
			case "tool_use":
				hasToolUse = true
			}
		}

		// If message contains tool_result blocks, they must be in a
		// separate user message. Flush any in-progress user message,
		// push the tool_result blocks alone, then continue accumulating
		// the non-tool_result parts under a fresh role.
		//
		// If the previous emitted message is already a user message of
		// pure tool_result blocks, merge into it — Claude requires all
		// tool_results for an assistant turn to live in a single user
		// message immediately following the tool_use, and consecutive
		// OpenAI tool messages are how multi-call responses arrive.
		if hasToolResult {
			toolResults := []claudeContent{}
			others := []claudeContent{}
			for _, b := range blocks {
				if b.Type == "tool_result" {
					toolResults = append(toolResults, b)
				} else {
					others = append(others, b)
				}
			}
			flush()
			if len(toolResults) > 0 {
				out = pushOrMergeToolResults(out, toolResults)
			}
			if len(others) > 0 {
				currentRole = newRole
				currentBlocks = append(currentBlocks, others...)
			}
			continue
		}

		if currentRole != newRole {
			flush()
			currentRole = newRole
		}
		currentBlocks = append(currentBlocks, blocks...)

		// After a tool_use, flush so the next message (which is expected
		// to be the tool result) is in its own message.
		if hasToolUse {
			flush()
		}
	}

	flush()
	return out, nil
}

func openaiMessageToBlocks(m oaiMessage) ([]claudeContent, error) {
	switch m.Role {
	case "tool":
		// tool_call_id + content → tool_result block
		return []claudeContent{{
			Type:      "tool_result",
			ToolUseID: m.ToolCallID,
			Content:   contentToStringRaw(m.Content),
		}}, nil
	case "user":
		return userContentToBlocks(m), nil
	case "assistant":
		return assistantContentToBlocks(m)
	default:
		// Unknown role: reject explicitly rather than silently
		// coercing to a text block. A typo or a future OpenAI role
		// should surface as a translation error so the caller can
		// fix the upstream request, not as an invalid Claude body.
		return nil, fmt.Errorf("unsupported role %q", m.Role)
	}
}

func userContentToBlocks(m oaiMessage) []claudeContent {
	s, parts, ok := splitContent(m.Content)
	if !ok {
		return nil
	}
	if s != "" {
		if s == "" {
			return nil
		}
		return []claudeContent{{Type: "text", Text: s}}
	}
	var blocks []claudeContent
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				blocks = append(blocks, claudeContent{Type: "text", Text: p.Text})
			}
		case "tool_result":
			blk := claudeContent{
				Type:      "tool_result",
				ToolUseID: p.ToolUseID,
				Content:   contentToStringRaw(p.Content),
			}
			if p.IsError != nil && *p.IsError {
				isErr := true
				blk.IsError = &isErr
			}
			blocks = append(blocks, blk)
		case "image_url":
			if p.ImageURL == nil || p.ImageURL.URL == "" {
				continue
			}
			if blk, ok := imageURLToImageBlock(p.ImageURL.URL); ok {
				blocks = append(blocks, blk)
			}
		case "image":
			if len(p.Source) > 0 {
				blocks = append(blocks, claudeContent{Type: "image", Source: p.Source})
			}
		}
	}
	return blocks
}

func assistantContentToBlocks(m oaiMessage) ([]claudeContent, error) {
	var blocks []claudeContent
	if s, parts, ok := splitContent(m.Content); ok {
		if s != "" {
			blocks = append(blocks, claudeContent{Type: "text", Text: s})
		}
		for _, p := range parts {
			switch p.Type {
			case "text":
				if p.Text != "" {
					blocks = append(blocks, claudeContent{Type: "text", Text: p.Text})
				}
			case "tool_use":
				blk := claudeContent{
					Type: "tool_use",
					ID:   p.ID,
					Name: p.Name,
				}
				if len(p.Input) > 0 {
					blk.Input = p.Input
				} else {
					blk.Input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, blk)
			case "thinking":
				// Strip cache_control if present (it's not a declared
				// field, so the typed decode drops it automatically).
				blocks = append(blocks, claudeContent{
					Type:      "thinking",
					Thinking:  p.Thinking,
					Signature: p.Signature,
				})
			}
		}
	}

	// OpenAI tool_calls → Claude tool_use blocks
	// Some models (e.g. tencent/hy3-free) concatenate multiple tool-call
	// arguments into one entry: {"cmd":"a"}{"cmd":"b"}. Split them apart.
	sepCount := make(map[string]int) // per-ID suffix counter
	for _, tc := range m.ToolCalls {
		parts := splitToolArgs(tc.Function.Arguments)
		for i, part := range parts {
			id := tc.ID
			if len(parts) > 1 {
				sepCount[tc.ID]++
				id = fmt.Sprintf("%s_%d", tc.ID, sepCount[tc.ID])
			}
			var input json.RawMessage
			if part != "" {
				input = json.RawMessage(part)
			}
			if i == 0 {
				// For the first part, fall through parseToolArgs for normal
				// validation ("" → {}, "null" → {}, non-object → {}).
				var err error
				input, err = parseToolArgs(tc.Function.Arguments)
				if err != nil {
					return nil, err
				}
			} else {
				// Extra split parts are already valid JSON from splitToolArgs.
				if len(part) == 0 || part == "null" || (len(part) > 0 && part[0] != '{') {
					input = json.RawMessage(`{}`)
				}
			}
			blocks = append(blocks, claudeContent{
				Type:  "tool_use",
				ID:    id,
				Name:  tc.Function.Name,
				Input: input,
			})
		}
	}
	return blocks, nil
}

// ============================================================================
// System prompt & response_format
// ============================================================================

func collectSystemParts(src oaiRequest) []string {
	var parts []string
	for _, m := range src.Messages {
		if m.Role != "system" {
			continue
		}
		s, parts2, ok := splitContent(m.Content)
		if !ok {
			continue
		}
		if s != "" {
			parts = append(parts, s)
			continue
		}
		for _, p := range parts2 {
			if p.Type == "text" && p.Text != "" {
				parts = append(parts, p.Text)
			}
		}
	}
	return parts
}

func convertResponseFormatToSystem(rf oaiResponseFormat) string {
	switch rf.Type {
	case "json_object":
		return "You must respond with valid JSON. Respond ONLY with a JSON object, no other text."
	case "json_schema":
		if rf.JSONSchema == nil || len(rf.JSONSchema.Schema) == 0 {
			return ""
		}
		// Marshal the schema to compact JSON. The result is wrapped
		// in a markdown code fence, so indentation is wasted CPU and
		// bytes on every request. If the input is already valid JSON,
		// pass it through; otherwise re-marshal via interface{} to
		// normalize spacing.
		schemaBytes := rf.JSONSchema.Schema
		if !json.Valid(schemaBytes) {
			var v any
			if err := json.Unmarshal(schemaBytes, &v); err != nil {
				return ""
			}
			if b, err := json.Marshal(v); err == nil {
				schemaBytes = b
			} else {
				return ""
			}
		}
		return fmt.Sprintf(
			"You must respond with valid JSON that strictly follows this JSON schema:\n```json\n%s\n```\nRespond ONLY with the JSON object, no other text.",
			string(schemaBytes),
		)
	}
	return ""
}

// ============================================================================
// Tool & tool_choice
// ============================================================================

// convertOaiTool converts an OpenAI tool definition to Claude form.
// Returns ok=false for tools that should be skipped (no name, or
// non-function built-ins that don't fit the typed struct).
func convertOaiTool(t oaiTool) (claudeTool, bool) {
	// Built-in non-function tool (e.g. web_search_20250305) — the
	// typed claudeTool struct can't represent it, so we drop it.
	// Callers can extend this if they need pass-through for specific
	// built-in types.
	if t.Type != "" && t.Type != "function" {
		return claudeTool{}, false
	}
	var name, desc string
	var params json.RawMessage
	if t.Function != nil {
		name = t.Function.Name
		desc = t.Function.Description
		params = t.Function.Parameters
	} else {
		// Already in Claude form (function absent).
		name = t.Name
		desc = t.Description
		params = t.InputSchema
	}
	if name == "" {
		return claudeTool{}, false
	}
	if len(params) == 0 {
		// Some clients put OpenAI's parameters at the top level when
		// there's no function wrapper.
		if len(t.Parameters) > 0 {
			params = t.Parameters
		} else {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
	}
	return claudeTool{
		Name:        name,
		Description: desc,
		InputSchema: params,
	}, true
}

func isToolChoiceNone(raw json.RawMessage) bool {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s == "none"
	}
	return false
}

func convertOpenAIToolChoice(raw json.RawMessage) json.RawMessage {
	out := claudeToolChoice{Type: "auto"}
	if len(raw) == 0 {
		return mustMarshal(out)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "required":
			out.Type = "any"
		}
		return mustMarshal(out)
	}
	var obj struct {
		Type     string `json:"type"`
		Name     string `json:"name,omitempty"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function,omitempty"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		switch obj.Type {
		case "auto", "any":
			out.Type = obj.Type
		case "tool":
			if obj.Name != "" {
				out.Type = "tool"
				out.Name = obj.Name
			}
		default:
			// OpenAI object shape: {type:"function", function:{name}}
			if obj.Function != nil && obj.Function.Name != "" {
				out.Type = "tool"
				out.Name = obj.Function.Name
			}
		}
	}
	return mustMarshal(out)
}

// ============================================================================
// Image, content, args helpers
// ============================================================================

func imageURLToImageBlock(url string) (claudeContent, bool) {
	const dataPrefix = "data:"
	if !strings.HasPrefix(url, dataPrefix) {
		// External http(s) URL — Claude supports source.url since 2024.
		if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
			return claudeContent{
				Type:   "image",
				Source: json.RawMessage(fmt.Sprintf(`{"type":"url","url":%q}`, url)),
			}, true
		}
		return claudeContent{}, false
	}
	// data:<media>;base64,<data>
	rest := strings.TrimPrefix(url, dataPrefix)
	parts := strings.SplitN(rest, ";", 2)
	if len(parts) != 2 {
		return claudeContent{}, false
	}
	mediaType := parts[0]
	encAndData := parts[1]
	const base64Prefix = "base64,"
	if !strings.HasPrefix(encAndData, base64Prefix) {
		return claudeContent{}, false
	}
	data := strings.TrimPrefix(encAndData, base64Prefix)
	if data == "" {
		return claudeContent{}, false
	}
	return claudeContent{
		Type: "image",
		Source: json.RawMessage(fmt.Sprintf(
			`{"type":"base64","media_type":%q,"data":%q}`, mediaType, data,
		)),
	}, true
}

func parseToolArgs(s string) (json.RawMessage, error) {
	if s == "" || s == "null" {
		return json.RawMessage(`{}`), nil
	}
	// Validate the JSON, then pass it through verbatim. s is a Go
	// string holding the JSON bytes the upstream produced; rejecting
	// malformed input here surfaces model bugs at the translator
	// boundary instead of silently calling the tool with `{}`.
	if !json.Valid([]byte(s)) {
		return nil, fmt.Errorf("parse tool arguments: invalid JSON")
	}
	// Tool input must be a JSON object. If the upstream produced a non-object
	// value (null, a number, a string, an array), normalize it to {} so
	// Claude Code and the Anthropic API don't reject it with a parse error.
	trimmed := strings.TrimSpace(s)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(s), nil
}

// contentToStringRaw converts a tool/content json.RawMessage (which can
// be a string, an array of content parts, or a single part) to a flat
// string for embedding in a tool_result block.
func contentToStringRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []oaiContentPart
	if err := json.Unmarshal(raw, &arr); err == nil {
		var parts []string
		for _, p := range arr {
			if p.Text != "" {
				parts = append(parts, p.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	// Try to decode as a single content part object before falling back
	// to the raw bytes. Without this, a single {"type":"text","text":"..."},
	// which is valid tool content but not a string or array, would be
	// returned as a raw JSON string — causing Claude Code to fail to parse
	// the tool_result content ("input JSON failed to parse").
	var single oaiContentPart
	if err := json.Unmarshal(raw, &single); err == nil && single.Text != "" {
		return single.Text
	}
	// Last resort: return the raw bytes as a string. This is a lossy fallback
	// but prevents a hard error. The caller (tool_result block) expects a
	// plain string; Claude Code will display whatever we send.
	return string(raw)
}

// ============================================================================
// Small utilities
// ============================================================================

// splitContent decodes an OAI content field (string or []oaiContentPart)
// into its two possible forms. ok=false if the content is neither shape.
func splitContent(raw json.RawMessage) (string, []oaiContentPart, bool) {
	if len(raw) == 0 {
		return "", nil, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil, true
	}
	var arr []oaiContentPart
	if err := json.Unmarshal(raw, &arr); err == nil {
		return "", arr, true
	}
	return "", nil, false
}

// mergeStop combines OpenAI's stop_sequences and stop into Claude's
// stop_sequences. OpenAI's stop takes precedence when present.
func mergeStop(stopSequences []string, stop json.RawMessage) []string {
	if len(stop) == 0 {
		return stopSequences
	}
	var s string
	if err := json.Unmarshal(stop, &s); err == nil {
		return []string{s}
	}
	var arr []string
	if err := json.Unmarshal(stop, &arr); err == nil {
		return arr
	}
	return stopSequences
}

// mustMarshal is a small helper for marshaling values where the only
// possible error is a programming bug (unmarshaleable type).
func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// pushOrMergeToolResults appends tool_result blocks to a Claude
// messages slice, merging into the previous user message when it
// already contains only tool_result blocks. Claude requires all
// tool_results responding to a single assistant tool_use turn to
// live in one user message; consecutive OpenAI tool messages
// (one per tool_call) are how multi-call responses arrive.
func pushOrMergeToolResults(out []claudeMessage, toolResults []claudeContent) []claudeMessage {
	if n := len(out); n > 0 {
		if out[n-1].Role == "user" && isAllToolResults(out[n-1].Content) {
			out[n-1].Content = append(out[n-1].Content, toolResults...)
			return out
		}
	}
	return append(out, claudeMessage{
		Role:    "user",
		Content: toolResults,
	})
}

func isAllToolResults(blocks []claudeContent) bool {
	for _, b := range blocks {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}

func reasoningEffortToBudget(effort string) int {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "xhigh":
		return 32768
	case "high":
		return 16384
	case "medium":
		return 8192
	case "low":
		return 4096
	case "none":
		return 0
	}
	return 0
}
