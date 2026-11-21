package domain

// Model describes a single chat-completion model exposed by an upstream
// provider, in OpenAI-compatible shape.
type Model struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	Created  int64  `json:"created"`
	OwnedBy  string `json:"owned_by"`
	IsFree   bool   `json:"is_free,omitempty"`
	Provider string `json:"provider,omitempty"`
	// ContextWindow is the model's total context size in tokens, as
	// published by the upstream catalog. Zero means unknown: omitempty
	// keeps it off the wire so clients can distinguish missing data from
	// an actual zero-sized window.
	ContextWindow int64 `json:"context_window,omitempty"`
	// Limit mirrors the native OpenCode limit object. Nil when the
	// upstream publishes no limit data.
	Limit *ModelLimit `json:"limit,omitempty"`
}

// ModelLimit is the token budget published by an upstream catalog.
// Fields are omitted when unknown (zero) so clients can tell the
// difference between "no data" and a real zero.
type ModelLimit struct {
	Context int64 `json:"context,omitempty"`
	Input   int64 `json:"input,omitempty"`
	Output  int64 `json:"output,omitempty"`
}

// ModelList is an OpenAI-compatible list of models.
type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// ErrorResp is the OpenAI-compatible error response envelope.
type ErrorResp struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the structured error body returned in ErrorResp.
type ErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
