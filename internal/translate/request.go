package translate

import (
	"fmt"

	"freegate/internal/translate/claude"
	"freegate/internal/translate/gemini"
	"freegate/internal/translate/internal/prepost"
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

// NormalizeRoles converts OpenAI "developer" role messages to "system"
// for upstream compatibility. Providers like DeepSeek do not support the
// "developer" role (introduced by OpenAI for o-series models). Since
// "developer" is semantically equivalent to "system", normalizing it
// before forwarding ensures broad compatibility.
//
// Uses a cheap byte scan to avoid JSON parsing when no "developer"
// substring exists in the body.
func NormalizeRoles(body []byte) ([]byte, error) {
	return prepost.NormalizeRoles(body)
}

func sourceToOpenAI(body []byte, source Format) ([]byte, error) {
	switch source {
	case FormatClaude:
		return claude.ToOpenAI(body)
	case FormatGemini:
		return gemini.ToOpenAI(body)
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
	default:
		// FormatOpenAI, "", or unknown — pass through.
		return body, nil
	}
}
