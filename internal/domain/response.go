package domain

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// UpstreamResponse is the domain-level response from an upstream provider.
// It decouples the application layer from net/http details while still
// carrying the streaming body needed for SSE.
type UpstreamResponse struct {
	Format     string
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

// freeTierProbeLimit bounds how much of an error body IsFreeTierRejection
// inspects. Rejection envelopes are small JSON objects; the marker sits
// near the start.
const freeTierProbeLimit = 8192

// peekBody wraps a probed response body: already-read bytes are replayed
// first, then the untouched tail, and Close closes the original body so
// no connection leaks.
type peekBody struct {
	reader io.Reader
	orig   io.ReadCloser
}

func (p *peekBody) Read(b []byte) (int, error) { return p.reader.Read(b) }
func (p *peekBody) Close() error {
	if p.orig == nil {
		return nil
	}
	return p.orig.Close()
}

// IsFreeTierRejection reports whether an upstream response is a free-tier
// access rejection (e.g. opencode.ai's FreeTierError, HTTP 403: the
// anonymous free tier now only serves requests from within OpenCode).
// Such a rejection is tier-scoped, not request-scoped: another candidate
// or combo tier may still serve, so callers treat it as failover-eligible
// like a 429/5xx rather than a pass-through 4xx.
//
// It peeks at most freeTierProbeLimit bytes for the marker and rewinds
// Body losslessly (peeked bytes + untouched tail), so the response stays
// fully readable and Close still releases the connection. Detection parses
// the error envelope (error.type / type) and falls back to substring match
// for non-JSON envelopes. Nil-safe: a nil response, nil body, or
// unreadable body reports false.
func IsFreeTierRejection(resp *UpstreamResponse) bool {
	if resp == nil || resp.Body == nil {
		return false
	}
	peek, err := io.ReadAll(io.LimitReader(resp.Body, freeTierProbeLimit+1))
	resp.Body = &peekBody{reader: io.MultiReader(bytes.NewReader(peek), resp.Body), orig: resp.Body}
	if err != nil {
		return false
	}
	if hasFreeTierMarker(peek) {
		return true
	}
	return bytes.Contains(peek, []byte("FreeTierError"))
}

// hasFreeTierMarker parses envelope shapes the gateway emits:
// {"error":{"type":"FreeTierError",...}} and {"type":"FreeTierError",...}.
func hasFreeTierMarker(peek []byte) bool {
	var probe struct {
		Type  string `json:"type"`
		Error *struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(peek, &probe); err != nil {
		return false
	}
	return probe.Type == "FreeTierError" || (probe.Error != nil && probe.Error.Type == "FreeTierError")
}
