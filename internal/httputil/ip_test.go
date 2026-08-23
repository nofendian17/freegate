package httputil

import (
	"net/http"
	"testing"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name   string
		trust  bool
		xff    string
		xri    string
		remote string
		want   string
	}{
		{"XFF trusted when trust enabled", true, "203.0.113.1", "", "10.0.0.1:1234", "203.0.113.1"},
		{"X-Real-IP trusted when trust enabled", true, "", "203.0.113.2", "10.0.0.1:1234", "203.0.113.2"},
		{"Multiple XFF takes first", true, "203.0.113.1, 198.51.100.1", "", "10.0.0.1:1234", "203.0.113.1"},
		{"No forwarded header falls back to RemoteAddr", true, "", "", "10.0.0.1:1234", "10.0.0.1"},

		{"Spoofed XFF ignored by default", false, "203.0.113.1", "", "10.0.0.1:1234", "10.0.0.1"},
		{"Spoofed X-Real-IP ignored by default", false, "", "203.0.113.2", "10.0.0.1:1234", "10.0.0.1"},
		{"RemoteAddr used by default", false, "", "", "10.0.0.1:1234", "10.0.0.1"},

		{"RemoteAddr no port fallback", false, "", "", "10.0.0.1", "10.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetTrustProxyHeaders(tt.trust)
			defer SetTrustProxyHeaders(false)
			r := &http.Request{RemoteAddr: tt.remote, Header: http.Header{}}
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				r.Header.Set("X-Real-IP", tt.xri)
			}
			got := ClientIP(r)
			if got != tt.want {
				t.Errorf("ClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTrustProxyHeaders_DefaultOff(t *testing.T) {
	defer SetTrustProxyHeaders(false)
	SetTrustProxyHeaders(true)
	if !TrustProxyHeaders() {
		t.Fatal("expected TrustProxyHeaders() = true after enabling")
	}
	SetTrustProxyHeaders(false)
	if TrustProxyHeaders() {
		t.Fatal("expected TrustProxyHeaders() = false after disabling")
	}
}
