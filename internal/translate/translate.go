// Package translate provides request and response format translation
// between OpenAI, Claude, and Gemini API formats.
//
// Architecture (hub-and-spoke):
//
//	Client (Claude/Gemini)         → Request Translation (→OpenAI) → Upstream
//	Client (Claude/Gemini) ← Response Translation (OpenAI→)       ← Upstream
//
// Default hub: upstreams speak OpenAI format; the translator converts
// to/from that intermediate format as needed. The package can
// additionally translate non-OpenAI upstreams for future use; see
// NewResponseWriterWithDst for the full two-direction signature.
//
// All translation goes through OpenAI as the intermediate format. For
// non-streaming requests/responses, this is a two-hop conversion (e.g.
// Claude JSON → OpenAI JSON → Gemini JSON). For streaming, only the
// directions whose endpoints are OpenAI (Claude→OpenAI, Gemini→OpenAI,
// OpenAI→Claude, OpenAI→Gemini) are supported in real time.
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
