package translate

import (
	"context"
	"fmt"

	"freegate/internal/translate/claude"
	"freegate/internal/translate/gemini"
	"freegate/internal/translate/internal/prepost"
	"freegate/internal/translate/responses"
)

// Request translates a request body from source format to target format.
//
// Translation is two-hop via OpenAI when neither side is OpenAI:
//
//	Claude → OpenAI → Gemini
//
// The order of pre-processing helpers applied to the OpenAI intermediate
// body is fixed:
//
//  0. NormalizeRoles          (also runs for same-format passthrough)
//  1. NormalizeThinkingConfig
//  2. SanitizeToolHistory
//  3. EnsureToolCallIds
//  4. FixMissingToolResponses
//  5. AdjustMaxTokens
//  6. PrepareClaudeRequest (only if target == FormatClaude)
//
// NormalizeRoles runs first and unconditionally (even when source == target)
// so that roles like "developer" are normalized to "system" before upstreams
// that do not support the "developer" role (e.g. DeepSeek) see them.
//
// SanitizeToolHistory runs next (after thinking normalization) to strip
// orphaned tool interactions at conversation edges. EnsureToolCallIds
// then sanitizes remaining tool-call ids. FixMissingToolResponses runs
// after both so any synthetic tool messages it inserts use the sanitized
// ids. AdjustMaxTokens runs last so it sees the final tools array and
// (possibly) inserted tool messages.
type requestFormatKey struct{}

func WithRequestFormat(ctx context.Context, format Format) context.Context {
	return context.WithValue(ctx, requestFormatKey{}, format)
}

func RequestFormat(ctx context.Context, body []byte) Format {
	if format, ok := ctx.Value(requestFormatKey{}).(Format); ok {
		return format
	}
	return Detect(body)
}

func Request(body []byte, source, target Format) ([]byte, error) {
	if source == target {
		return prepost.NormalizeRoles(body)
	}

	// Step 1: source → OpenAI.
	out, err := sourceToOpenAI(body, source)
	if err != nil {
		return nil, err
	}

	// Step 2: pre-processing on the OpenAI body.
	out, err = prepost.NormalizeRoles(out)
	if err != nil {
		return nil, fmt.Errorf("translate: normalize roles: %w", err)
	}
	out, err = prepost.NormalizeThinkingConfig(out)
	if err != nil {
		return nil, fmt.Errorf("translate: normalize thinking: %w", err)
	}
	out, err = prepost.SanitizeToolHistory(out)
	if err != nil {
		return nil, fmt.Errorf("translate: sanitize tool history: %w", err)
	}
	out, err = prepost.EnsureToolCallIds(out)
	if err != nil {
		return nil, fmt.Errorf("translate: ensure tool call ids: %w", err)
	}
	out, err = prepost.FixMissingToolResponses(out)
	if err != nil {
		return nil, fmt.Errorf("translate: fix missing tool responses: %w", err)
	}
	out, err = prepost.AdjustMaxTokens(out)
	if err != nil {
		return nil, fmt.Errorf("translate: adjust max tokens: %w", err)
	}

	// Step 3: OpenAI → target.
	out, err = openAIToTarget(out, target)
	if err != nil {
		return nil, err
	}

	// Step 4: Claude-specific finalization.
	if target == FormatClaude {
		out, err = prepost.PrepareClaudeRequest(out)
		if err != nil {
			return nil, fmt.Errorf("translate: prepare claude request: %w", err)
		}
	}

	return out, nil
}

// PrepareForUpstreamWithModel applies request normalization for
// upstream-bound bodies in a single JSON pass: developer→system roles,
// reasoning→reasoning_content, and stream_options for streams, plus
// DeepSeek model-aware normalization: every assistant message gets
// reasoning_content ("" when absent) and flash models without top_p get
// 0.95. It mirrors opencode's deepseek/deepseek-v4-flash handling in
// packages/opencode/src/provider/transform.ts (normalizeMessages,
// topP) translated to OpenAI-compatible bodies.
func PrepareForUpstreamWithModel(body []byte, modelID string) ([]byte, []string, error) {
	return prepost.PrepareUpstreamWithModel(body, modelID)
}

// NormalizedHeader is the response header listing the request
// normalizations that fired for the upstream-bound body, as a
// comma-separated token list (see the prepost.Applied* constants).
// It is set only when at least one normalization applied.
const NormalizedHeader = "X-Fg-Normalized"

// NormalizeClaudeContent strips content blocks the Anthropic API rejects
// (empty text, unsigned empty thinking, empty redacted_thinking) and drops
// messages left empty, keeping a final assistant. It mirrors opencode's
// empty-part filter for @ai-sdk/anthropic in
// packages/opencode/src/provider/transform.ts (normalizeMessages).
func NormalizeClaudeContent(body []byte) ([]byte, []string, error) {
	return prepost.NormalizeClaudeContent(body)
}

func sourceToOpenAI(body []byte, source Format) ([]byte, error) {
	switch source {
	case FormatClaude:
		return claude.ToOpenAI(body)
	case FormatGemini:
		return gemini.ToOpenAI(body)
	case FormatOpenAIResponses:
		return responses.ToOpenAI(body)
	default:
		// FormatOpenAI, "", or unknown — pass through.
		return body, nil
	}
}

func openAIToTarget(body []byte, target Format) ([]byte, error) {
	switch target {
	case FormatClaude:
		return claude.FromOpenAI(body)
	case FormatGemini:
		return gemini.FromOpenAI(body)
	case FormatOpenAIResponses:
		return responses.FromOpenAI(body)
	default:
		// FormatOpenAI, "", or unknown — pass through.
		return body, nil
	}
}
