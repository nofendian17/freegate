package domain

// Model describes a single chat-completion model exposed by an upstream
// provider, in OpenAI-compatible shape.
type Model struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	OwnedBy   string `json:"owned_by"`
	Provider  string `json:"-"`
	Created   int64  `json:"created,omitempty"`
	BadgeURL  string `json:"badge_url,omitempty"`
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
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// NewError builds an ErrorResp with the given type, message, and code.
func NewError(typ, msg, code string) ErrorResp {
	return ErrorResp{Error: ErrorDetail{Type: typ, Message: msg, Code: code}}
}
