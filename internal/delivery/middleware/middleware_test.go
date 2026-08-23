package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"freegate/internal/httputil"
)

func TestRateLimiter_FirstRequest(t *testing.T) {
	rl := NewRateLimiter(5)
	defer rl.Stop()

	ip := "192.168.1.1"
	if !rl.allow(ip) {
		t.Fatal("expected first request to be allowed")
	}
}

func TestRateLimiter_UnderLimit(t *testing.T) {
	rl := NewRateLimiter(5)
	defer rl.Stop()

	ip := "192.168.1.2"
	for range 4 {
		if !rl.allow(ip) {
			t.Fatal("expected request under limit to be allowed")
		}
	}
}

func TestRateLimiter_ExceedsLimit(t *testing.T) {
	rl := NewRateLimiter(3)
	defer rl.Stop()

	ip := "192.168.1.3"
	for range 3 {
		rl.allow(ip)
	}
	if rl.allow(ip) {
		t.Fatal("expected request after limit to be denied")
	}
}

func TestRateLimiter_ResetAfterMinute(t *testing.T) {
	rl := NewRateLimiter(1)
	defer rl.Stop()

	ip := "192.168.1.4"
	if !rl.allow(ip) {
		t.Fatal("expected first request to be allowed")
	}
	if rl.allow(ip) {
		t.Fatal("expected second request to be denied")
	}

	// Manually set lastSeen to 61 seconds ago
	sh := rl.shardFor(ip)
	sh.mu.Lock()
	v := sh.visitors[ip]
	v.lastSeen = time.Now().Add(-61 * time.Second)
	sh.mu.Unlock()

	if !rl.allow(ip) {
		t.Fatal("expected request after reset to be allowed")
	}
}

// TestRateLimiter_SpoofedForwardedHeaderIgnoredByDefault guards against
// X-Forwarded-For spoofing: with TRUST_PROXY_HEADERS off (the default), a
// client must not escape its per-IP bucket by rotating forwarded headers.
func TestRateLimiter_SpoofedForwardedHeaderIgnoredByDefault(t *testing.T) {
	httputil.SetTrustProxyHeaders(false)
	defer httputil.SetTrustProxyHeaders(false)

	rl := NewRateLimiter(1)
	defer rl.Stop()

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req1 := httptest.NewRequest("GET", "/v1/models", nil)
	req1.RemoteAddr = "10.0.0.9:1000"
	req1.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec1.Code)
	}

	// Same RemoteAddr, different spoofed XFF → same bucket, rate limited.
	req2 := httptest.NewRequest("GET", "/v1/models", nil)
	req2.RemoteAddr = "10.0.0.9:1000"
	req2.Header.Set("X-Forwarded-For", "203.0.113.10")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("spoofed XFF bypassed the limiter: status = %d, want 429", rec2.Code)
	}
}

// TestRateLimiter_TrustProxyHeadersHonorsXFF verifies that when
// TRUST_PROXY_HEADERS is enabled, clients behind a reverse proxy get their
// own bucket keyed by the XFF first hop instead of the proxy's RemoteAddr.
func TestRateLimiter_TrustProxyHeadersHonorsXFF(t *testing.T) {
	httputil.SetTrustProxyHeaders(true)
	defer httputil.SetTrustProxyHeaders(false)

	rl := NewRateLimiter(1)
	defer rl.Stop()

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	mkReq := func(xff string) *http.Request {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		req.RemoteAddr = "10.0.0.9:1000"
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		return req
	}

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, mkReq("203.0.113.9"))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first proxied request status = %d, want 200", rec1.Code)
	}

	// Same XFF client over a fresh proxy connection → same bucket.
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, mkReq("203.0.113.9"))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("same proxied client: status = %d, want 429", rec2.Code)
	}

	// A different real client behind the proxy → its own bucket.
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, mkReq("203.0.113.11"))
	if rec3.Code != http.StatusOK {
		t.Fatalf("different proxied client: status = %d, want 200", rec3.Code)
	}
}

func TestAuth_SkipWhenEmpty(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := Auth("")
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestAuth_ValidKey(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := Auth("secret-key")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "secret-key")
	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestAuth_InvalidKey(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := Auth("secret-key")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuth_ValidBearerToken(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := Auth("secret-key")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRequestID_GeneratesWhenMissing(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			t.Error("expected request ID to be set")
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	RequestID(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID header in response")
	}
}

func TestRequestID_PreservesExisting(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id != "client-provided-id" {
			t.Errorf("expected client-provided-id, got %s", id)
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", "client-provided-id")
	rec := httptest.NewRecorder()
	RequestID(handler).ServeHTTP(rec, req)

	if rec.Header().Get("X-Request-ID") != "client-provided-id" {
		t.Fatalf("expected client-provided-id, got %s", rec.Header().Get("X-Request-ID"))
	}
}

func TestLogger_LogsNormalRequest(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	Logger(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(buf.String(), "request") {
		t.Fatalf("expected a request log line, got: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "status=200") {
		t.Fatalf("expected logged status 200, got: %q", buf.String())
	}
}

func TestLogger_SkipsDashboardAndProbeNoise(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"dashboard root", "/"},
		{"htmx stats partial", "/partials/stats"},
		{"dashboard json api", "/api/health"},
		{"vpn status poll", "/api/vpn/status"},
		{"static asset", "/static/css/app.css"},
		{"metrics endpoint", "/v1/metrics"},
		{"ready probe", "/v1/ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			defer slog.SetDefault(old)

			ran := false
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ran = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest("GET", tt.path, nil)
			rec := httptest.NewRecorder()
			Logger(handler).ServeHTTP(rec, req)

			if !ran {
				t.Fatal("expected the handler to run for the request")
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			if strings.Contains(buf.String(), "request") {
				t.Fatalf("expected no request log line for %q, got: %q", tt.path, buf.String())
			}
		})
	}
}

func TestApiAuth_MultiKey(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	m := ApiAuth([]string{"k1", "k2"}, "admin12345678901234")
	for _, k := range []string{"k1", "k2"} {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		req.Header.Set("X-API-Key", k)
		rec := httptest.NewRecorder()
		m(h).ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("key %s should pass, got %d", k, rec.Code)
		}
	}
	// admin superset via Bearer
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer admin12345678901234")
	rec := httptest.NewRecorder()
	m(h).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("admin should pass api, got %d", rec.Code)
	}
}

func TestApiAuth_AdminSessionCookie(t *testing.T) {
	admin := "0123456789abcdef0123456789abcdef"
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	m := ApiAuth([]string{"k1"}, admin)

	// valid fg_admin cookie (set by POST /login) grants /v1 without headers
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.AddCookie(&http.Cookie{Name: "fg_admin", Value: HmacForToken(admin)})
	rec := httptest.NewRecorder()
	m(h).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("admin session cookie should pass /v1, got %d", rec.Code)
	}

	// tampered cookie still rejected
	bad := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	bad.AddCookie(&http.Cookie{Name: "fg_admin", Value: "deadbeef"})
	recBad := httptest.NewRecorder()
	m(h).ServeHTTP(recBad, bad)
	if recBad.Code != 401 {
		t.Fatalf("tampered cookie should 401, got %d", recBad.Code)
	}
}

func TestAdminAuth_Cookie(t *testing.T) {
	admin := "0123456789abcdef0123456789abcdef"
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	m := AdminAuth(admin)
	// no cookie -> redirect
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	m(h).ServeHTTP(rec, req)
	if rec.Code != 302 {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	// with valid cookie
	cookieVal := hmacForToken(admin) // same logic as login
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.AddCookie(&http.Cookie{Name: "fg_admin", Value: cookieVal})
	rec2 := httptest.NewRecorder()
	m(h).ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("valid cookie should pass, got %d", rec2.Code)
	}
}
