package domain

import (
	"context"
	"net/http"
)

type ChatRequest struct {
	Body        []byte
	OriginalReq *http.Request
}

type Upstream interface {
	Name() string
	Match(modelID string) bool
	ListModels(ctx context.Context) ([]Model, error)
	ChatCompletion(ctx context.Context, req ChatRequest) (*http.Response, error)
	Models() []Model
	Start(ctx context.Context)
}
