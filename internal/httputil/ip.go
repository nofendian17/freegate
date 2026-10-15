package httputil

import (
	"net"
	"net/http"
	"strings"
	"sync/atomic"
)

// trustProxyHeaders controls whether ClientIP honors X-Forwarded-For /
// X-Real-IP. It is off by default: when freegate is exposed directly (or
// bound to localhost), those headers are client-controlled and spoofable,
// so trusting them would let callers evade per-IP rate limits and pollute
// logged IPs. Enable it only when freegate runs behind a reverse proxy
// that overwrites these headers.
var trustProxyHeaders atomic.Bool

// SetTrustProxyHeaders configures whether forwarded headers are trusted.
// Call it once during bootstrap from configuration (TRUST_PROXY_HEADERS).
func SetTrustProxyHeaders(trust bool) { trustProxyHeaders.Store(trust) }

// ClientIP returns the originating client IP for r. When proxy headers are
// trusted, it prefers the rightmost X-Forwarded-For hop — the address the
// nearest appending proxy vouched for — then X-Real-IP; otherwise it always
// uses the host portion of RemoteAddr.
func ClientIP(r *http.Request) string {
	if trustProxyHeaders.Load() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Take the rightmost hop: appending proxies (nginx, HAProxy's
			// default) append the peer they accepted the connection from,
			// so the final entry is the only hop the chain vouches for.
			// Earlier entries are caller-supplied text and trivially
			// spoofed to rotate per-IP rate limits and throttles.
			if i := strings.LastIndex(xff, ","); i >= 0 {
				return strings.TrimSpace(xff[i+1:])
			}
			return strings.TrimSpace(xff)
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
