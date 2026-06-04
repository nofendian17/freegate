package httputil

import (
	"net/http"
	"testing"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name   string
		xff    string
		xri    string
		remote string
		want   string
	}{
		{"X-Forwarded-For trusted", "203.0.113.1", "", "10.0.0.1:1234", "203.0.113.1"},
		{"X-Real-IP", "", "203.0.113.2", "10.0.0.1:1234", "203.0.113.2"},
		{"No forwarded header", "", "", "10.0.0.1:1234", "10.0.0.1"},
		{"RemoteAddr no port (SplitHostPort err)", "", "", "10.0.0.1", "10.0.0.1"},
		{"Multiple XFF takes first", "203.0.113.1, 198.51.100.1", "", "10.0.0.1:1234", "203.0.113.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
