// Package reasonctx provides context helpers for storing and retrieving
// assistant reasoning_content through the request lifecycle.
package reasonctx

import "context"

// reasoningKey is the context key for storing assistant reasoning content.
type reasoningKey struct{}

// ReasoningData maps message index → reasoning_content string for assistant
// messages that need reasoning_content re-injected at the HTTP transport layer.
type ReasoningData map[int]string

// ContextWithReasoning stores reasoning data in the context so the custom
// HTTP transport can inject reasoning_content into outgoing request bodies.
func ContextWithReasoning(ctx context.Context, data ReasoningData) context.Context {
	return context.WithValue(ctx, reasoningKey{}, data)
}

// ReasoningFromContext retrieves reasoning data from the context, or nil.
func ReasoningFromContext(ctx context.Context) ReasoningData {
	v, _ := ctx.Value(reasoningKey{}).(ReasoningData)
	return v
}
