package model

import "errors"

var (
	ErrModelNotFound    = errors.New("model not found")
	ErrEmptyRequestBody = errors.New("empty request body")
	ErrBodyTooLarge     = errors.New("request body too large")
)

type Model struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	Created  int64  `json:"created"`
	OwnedBy  string `json:"owned_by"`
	IsFree   bool   `json:"is_free,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

type ErrorResp struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func NewError(tp, msg string) ErrorResp {
	var e ErrorResp
	e.Error.Type = tp
	e.Error.Message = msg
	return e
}
