package domain

import (
	"io"
	"net/http"
)

// UpstreamResponse is the domain-level response from an upstream provider.
// It decouples the application layer from net/http details while still
// carrying the streaming body needed for SSE.
type UpstreamResponse struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

// NewUpstreamResponse wraps an http.Response into a domain UpstreamResponse.
// The caller retains ownership of Body and must Close().
func NewUpstreamResponse(resp *http.Response) *UpstreamResponse {
	if resp == nil {
		return nil
	}
	return &UpstreamResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       resp.Body,
	}
}

// Close closes the underlying body.
func (r *UpstreamResponse) Close() error {
	if r == nil || r.Body == nil {
		return nil
	}
	return r.Body.Close()
}
