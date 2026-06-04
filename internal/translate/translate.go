// Package translate provides request and response format translation
// between OpenAI, Claude, and Gemini API formats.
//
// Architecture (hub-and-spoke):
//
//	Client (Claude/Gemini)         → Request Translation (→OpenAI) → Upstream
//	Client (Claude/Gemini) ← Response Translation (OpenAI→)       ← Upstream
//
// All upstreams speak OpenAI format; the translator converts to/from
// that intermediate format as needed.
package translate

import (
	"errors"
)

// Format identifies the API request/response format.
type Format string

const (
	FormatOpenAI Format = "openai"
	FormatClaude Format = "claude"
	FormatGemini Format = "gemini"
)

var (
	ErrUnsupportedTranslation = errors.New("translate: unsupported format pair")
	ErrInvalidBody            = errors.New("translate: invalid request body")
)
