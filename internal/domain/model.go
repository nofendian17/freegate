package domain

type Model struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	OwnedBy  string `json:"owned_by"`
	Provider string `json:"-"`
	Created  int64  `json:"created,omitempty"`
	BadgeURL string `json:"badge_url,omitempty"`
}

type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

type ErrorResp struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

func NewError(typ, msg, code string) ErrorResp {
	return ErrorResp{Error: ErrorDetail{Type: typ, Message: msg, Code: code}}
}
