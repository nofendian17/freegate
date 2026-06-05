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
//  1. NormalizeThinkingConfig
//  2. EnsureToolCallIds
//  3. FixMissingToolResponses
//  4. AdjustMaxTokens
//  5. PrepareClaudeRequest (only if target == FormatClaude)
//
// FixMissingToolResponses runs AFTER EnsureToolCallIds so any synthetic
// tool messages we insert use the sanitized ids. AdjustMaxTokens runs
// last so it sees the final tools array and (possibly) inserted tool
// messages.
func Request(body []byte, source, target Format) ([]byte, error) {
	if source == target {
		return body, nil
	}

	// Step 1: source → OpenAI.
	out, err := sourceToOpenAI(body, source)
	if err != nil {
		return nil, err
	}

	// Step 2: pre-processing on the OpenAI body.
	out, err = prepost.NormalizeThinkingConfig(out)
	if err != nil {
		return nil, fmt.Errorf("translate: normalize thinking: %w", err)
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
