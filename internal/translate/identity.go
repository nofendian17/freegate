package translate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// Downstream Zen identity plumbing, mirroring 9router's session handling
// (PR #10): when the downstream client is itself an OpenCode-compatible
// caller (e.g. the genuine CLI chained through freegate), its identity
// headers are forwarded upstream instead of minting fresh ones, so the
// gateway sees the original client.

const (
	// MinOpencodeClientMajor/MinOpencodeClientMinor is the gateway floor:
	// User-Agents below opencode/1.17.x are rejected (426/403).
	MinOpencodeClientMajor = 1
	MinOpencodeClientMinor = 17
	// maxIdentityHeaderLen bounds forwarded identity headers.
	maxIdentityHeaderLen = 256
)

// OpenCodeSessionRE is the canonical descending session form:
// ses_ + 12 lowercase hex timestamp digits + 14 Base62 chars.
var OpenCodeSessionRE = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

var opencodeVersionRE = regexp.MustCompile(`(?i)opencode/(\d+)\.(\d+)(?:\.(\d+))?`)

const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// DownstreamIdentity carries the Zen identity headers a downstream client
// sent with its request. Empty fields mean "not provided".
type DownstreamIdentity struct {
	UserAgent string
	Session   string
	RequestID string
	Client    string
	Project   string
}

type identityKey struct{}

// WithDownstreamIdentity stores the downstream identity on ctx.
func WithDownstreamIdentity(ctx context.Context, id DownstreamIdentity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// DownstreamIdentityFrom returns the stored identity, or the zero value
// when none was provided.
func DownstreamIdentityFrom(ctx context.Context) DownstreamIdentity {
	if id, ok := ctx.Value(identityKey{}).(DownstreamIdentity); ok {
		return id
	}
	return DownstreamIdentity{}
}

// ClipIdentity trims whitespace and caps lengths so oversized
// client-controlled values never reach upstream headers.
func ClipIdentity(id DownstreamIdentity) DownstreamIdentity {
	clip := func(s string) string {
		s = strings.TrimSpace(s)
		if len(s) > maxIdentityHeaderLen {
			s = s[:maxIdentityHeaderLen]
		}
		return s
	}
	return DownstreamIdentity{
		UserAgent: clip(id.UserAgent),
		Session:   clip(id.Session),
		RequestID: clip(id.RequestID),
		Client:    clip(id.Client),
		Project:   clip(id.Project),
	}
}

// ValidOpencodeVersion reports whether ua carries opencode/<major>.<minor>
// at or above the gateway floor. Unanchored on purpose: genuine clients
// append provider-utils/runtime suffixes.
func ValidOpencodeVersion(ua string) bool {
	m := opencodeVersionRE.FindStringSubmatch(ua)
	if m == nil {
		return false
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return false
	}
	return major > MinOpencodeClientMajor || (major == MinOpencodeClientMajor && minor >= MinOpencodeClientMinor)
}

// TranslateSessionID maps any downstream session to canonical form.
// Canonical input passes through verbatim; anything else is deterministically
// derived via SHA-256, so the same downstream identity always maps to the
// same upstream session (mirrors 9router translateSessionId).
func TranslateSessionID(sessionID, clientTool string) string {
	sessionID = strings.TrimSpace(sessionID)
	if OpenCodeSessionRE.MatchString(sessionID) {
		return sessionID
	}
	if clientTool == "" {
		clientTool = "generic"
	}
	sum := sha256.Sum256([]byte("opencode\x00" + clientTool + "\x00" + sessionID))
	var sb strings.Builder
	sb.WriteString("ses_")
	sb.WriteString(hex.EncodeToString(sum[:6]))
	for _, b := range sum[6:20] {
		sb.WriteByte(base62Chars[int(b)%62])
	}
	return sb.String()
}
