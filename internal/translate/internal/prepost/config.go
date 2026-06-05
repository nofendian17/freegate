// Package prepost provides cross-format pre/post-processing helpers used by
// the translate package. It is internal: only code under
// freegate/internal/translate/... may import it.
package prepost

// Token-budget defaults applied by AdjustMaxTokens.
const (
	// DefaultMaxTokens is the fallback max_tokens used when the client did
	// not provide one. Mirrors 9router's runtimeConfig DEFAULT_MAX_TOKENS.
	DefaultMaxTokens = 4096

	// DefaultMinTokens is the minimum max_tokens required when tools are
	// present (to avoid truncating tool-call arguments).
	DefaultMinTokens = 4096
)

// AnthropicToolIDPattern is the regex pattern required by the Anthropic
// API for tool_use.id and tool_call_id fields. Source:
// https://docs.anthropic.com/en/api/messages#tool-use
const AnthropicToolIDPattern = `^[a-zA-Z0-9_-]+$`
