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

// NewError builds an ErrorResp with the given type and message.
func NewError(tp, msg string) ErrorResp {
	return ErrorResp{Error: ErrorDetail{Type: tp, Message: msg}}
}
