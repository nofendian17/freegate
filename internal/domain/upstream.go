package domain

import (
	"context"
	"net/http"
)

// ChatRequest is what the application layer hands to an Upstream's
// ChatCompletion method. Body is the raw, already-parsed request body;
// OriginalReq is the inbound HTTP request, kept so the upstream can
// honor context (timeouts, cancelation) and headers.
type ChatRequest struct {
	Body        []byte
	OriginalReq *http.Request
}

// Upstream is the port through which the application layer talks to a
// specific AI provider (OpenCode, Kilo, ...).
type Upstream interface {
	// Name returns the provider name (e.g. "opencode", "kilo").
	Name() string
	// Match reports whether this upstream serves the given model ID.
	Match(modelID string) bool
	// ListModels fetches the current model catalog from the provider.
	ListModels(ctx context.Context) ([]Model, error)
	// ChatCompletion performs a single chat-completion request. The
	// caller owns the returned response and must close its body.
	ChatCompletion(ctx context.Context, req ChatRequest) (*http.Response, error)
	// Models returns the most recently cached catalog without a network call.
	Models() []Model
	// Start launches any background refreshers.
	Start(ctx context.Context)
}
