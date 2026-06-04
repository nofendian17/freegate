package domain

import "errors"

var (
	ErrModelNotFound    = errors.New("model not found")
	ErrEmptyRequestBody = errors.New("empty request body")
	ErrBodyTooLarge     = errors.New("request body too large")
)
