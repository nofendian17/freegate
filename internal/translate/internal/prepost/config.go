// Package prepost provides cross-format pre/post-processing helpers used by
// the translate package. It is internal: only code under
// freegate/internal/translate/... may import it.
package prepost

// Token-budget defaults applied by AdjustMaxTokens.
const (
	// DefaultMaxTokens is the fallback max_tokens used when the client did
	// not provide one.
	DefaultMaxTokens = 4096

	// DefaultMinTokens is the minimum max_tokens required when tools are
	// present (to avoid truncating tool-call arguments).
	DefaultMinTokens = 4096
)

// Applied-normalization tokens reported by PrepareUpstreamWithModel and
// NormalizeClaudeContent. They surface to clients via the X-Fg-Normalized
// response header so operators can see which normalizations fired.
const (
	// AppliedDeveloperSystem marks developer -> system role rewrites.
	AppliedDeveloperSystem = "developer-system"
	// AppliedReasoningContent marks reasoning -> reasoning_content copies
	// (any model) and empty reasoning_content guarantees (DeepSeek).
	AppliedReasoningContent = "reasoning-content"
	// AppliedStreamOptions marks stream_options injection for streams.
	AppliedStreamOptions = "stream-options"
	// AppliedDeepSeekFlashTopP marks the flash top_p default.
	AppliedDeepSeekFlashTopP = "deepseek-flash-top-p"
	// AppliedClaudeStripEmpty marks stripped Anthropic-rejected blocks
	// and dropped emptied messages.
	AppliedClaudeStripEmpty = "claude-strip-empty"
)

// AnthropicToolIDPattern is the regex pattern required by the Anthropic
// API for tool_use.id and tool_call_id fields. Source:
// https://docs.anthropic.com/en/api/messages#tool-use
const AnthropicToolIDPattern = `^[a-zA-Z0-9_-]+$`
