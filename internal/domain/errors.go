package domain

import "errors"

// Domain-level errors used across the application and delivery layers.
var (
	ErrModelNotFound    = errors.New("model not found")
	ErrEmptyRequestBody = errors.New("empty request body")
	ErrBodyTooLarge     = errors.New("request body too large")
)
