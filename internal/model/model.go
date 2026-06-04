package model

import "freegate/internal/domain"

type (
	Model       = domain.Model
	ModelList   = domain.ModelList
	ErrorResp   = domain.ErrorResp
	ErrorDetail = domain.ErrorDetail
)

var (
	NewError            = domain.NewError
	ErrModelNotFound    = domain.ErrModelNotFound
	ErrEmptyRequestBody = domain.ErrEmptyRequestBody
	ErrBodyTooLarge     = domain.ErrBodyTooLarge
)
