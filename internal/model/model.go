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

type KiloModelList struct {
	Object string      `json:"object"`
	Data   []KiloModel `json:"data"`
}

type KiloModel struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Created       int64       `json:"created"`
	Description   string      `json:"description"`
	ContextLength int         `json:"context_length"`
	Pricing       KiloPricing `json:"pricing"`
	IsFree        bool        `json:"isFree"`
}

type KiloPricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

type OpenCodeModelList struct {
	Object string          `json:"object"`
	Data   []OpenCodeModel `json:"data"`
}

type OpenCodeModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Cost    string `json:"cost"`
}
