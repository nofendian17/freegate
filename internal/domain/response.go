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
	// Substring scan covers both JSON envelopes and non-JSON bodies, and
	// also succeeds on truncated large bodies where json.Unmarshal would
	// fail on the cut-off tail. Keep the structured parse as a complement
	// for escaped/odd encodings, but never let it veto a direct hit.
	if bytes.Contains(peek, []byte("FreeTierError")) {
		return true
	}
	return hasFreeTierMarker(peek)
}

// freeUsageProbeLimit bounds the body scan for a free-usage 429.
const freeUsageProbeLimit = 8192

// IsFreeUsageLimit reports whether an upstream response is OpenCode's
// free-tier usage limit (HTTP 429, FreeUsageLimitError). The limit is counted
// per internet address, so a caller that can change its exit IP may retry the
// same request after rotating. Generic 429s (per-key rate limits, "too many
// requests") are deliberately not matched: rotating for those would burn exit
// identities without fixing anything.
//
// The body is peeked and rewound exactly like IsFreeTierRejection, so callers
// can still read or close the response normally.
func IsFreeUsageLimit(resp *UpstreamResponse) bool {
	if resp == nil || resp.Body == nil || resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	peek, err := io.ReadAll(io.LimitReader(resp.Body, freeUsageProbeLimit+1))
	resp.Body = &peekBody{reader: io.MultiReader(bytes.NewReader(peek), resp.Body), orig: resp.Body}
	if err != nil {
		return false
	}
	low := bytes.ToLower(peek)
	for _, marker := range [][]byte{
		[]byte("freeusagelimiterror"),
		[]byte("free usage limit"),
		[]byte("free usage exceeded"),
		[]byte("free-tier usage limit"),
		[]byte("quota exceeded"),
		[]byte("resource_exhausted"),
	} {
		if bytes.Contains(low, marker) {
			return true
		}
	}
	return false
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

// IsRetryableStatus reports whether a response warrants failover to the next
// upstream candidate: transport-level retryables (429/5xx) plus free-tier
// access rejections, which are scoped to a specific upstream rather than the
// request as a whole. Nil-safe: a nil response counts as retryable.
//
// The free-tier probe peeks at most 8KB and rewinds the body; the marker
// scan inside IsFreeTierRejection fast-paths on substring before JSON.
func IsRetryableStatus(rsp *UpstreamResponse) bool {
	if rsp == nil {
		return true
	}
	if rsp.StatusCode == http.StatusTooManyRequests || rsp.StatusCode >= 500 {
		return true
	}
	return rsp.StatusCode >= 400 && IsFreeTierRejection(rsp)
}
