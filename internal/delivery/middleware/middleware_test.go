package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	rl.mu.Lock()
	v := rl.visitors[ip]
	v.lastSeen = time.Now().Add(-61 * time.Second)
	rl.mu.Unlock()

	if !rl.allow(ip) {
		t.Fatal("expected request after reset to be allowed")
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

	req := httptest.NewRequest("GET", "/", nil)
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

func TestLogger_SkipsHXRequest(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	ran := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	Logger(handler).ServeHTTP(rec, req)

	if !ran {
		t.Fatal("expected the handler to run for HX request")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.Contains(buf.String(), "request") {
		t.Fatalf("expected no request log line for HX request, got: %q", buf.String())
	}
}
