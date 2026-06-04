package model

import "freegate/internal/domain"

type (
	Model     = domain.Model
	ModelList = domain.ModelList
	ErrorResp = domain.ErrorResp
)

var (
	NewError            = domain.NewError
	ErrModelNotFound    = domain.ErrModelNotFound
	ErrEmptyRequestBody = domain.ErrEmptyRequestBody
	ErrBodyTooLarge     = domain.ErrBodyTooLarge
)
