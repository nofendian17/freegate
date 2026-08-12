package vpngate

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// newMockServer serves the supervisor control API and counts /rotate calls.
func newMockServer(rotateHandler http.HandlerFunc, ip string) (*httptest.Server, *int32) {
	var rotates int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rotate":
			atomic.AddInt32(&rotates, 1)
			if rotateHandler != nil {
				rotateHandler(w, r)
				return
			}
			writeTestJSON(w, map[string]string{"ip": ip})
		case "/ip":
			writeTestJSON(w, map[string]string{"ip": ip})
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, &rotates
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func splitAddr(rawURL string) (string, int) {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		panic(err)
	}
	p, _ := strconv.Atoi(portStr)
	return host, p
}

func TestNewIPRotates(t *testing.T) {
	srv, rotates := newMockServer(nil, "1.2.3.4")
	defer srv.Close()

	host, port := splitAddr(srv.URL)
	c := NewController(host, port, time.Hour)

	if err := c.NewIP(); err != nil {
		t.Fatalf("NewIP failed: %v", err)
	}
	if got := c.CurrentIP(); got != "1.2.3.4" {
		t.Fatalf("CurrentIP = %q, want %q", got, "1.2.3.4")
	}
	if n := atomic.LoadInt32(rotates); n != 1 {
		t.Fatalf("expected 1 rotate call, got %d", n)
	}
}

func TestNewIPThrottles(t *testing.T) {
	srv, rotates := newMockServer(nil, "1.2.3.4")
	defer srv.Close()

	host, port := splitAddr(srv.URL)
	c := NewController(host, port, time.Hour)

	if err := c.NewIP(); err != nil {
		t.Fatalf("first NewIP failed: %v", err)
	}
	if err := c.NewIP(); err != nil {
		t.Fatalf("second NewIP failed: %v", err)
	}
	// Second call must be skipped by the minimum interval.
	if n := atomic.LoadInt32(rotates); n != 1 {
		t.Fatalf("expected 1 rotate call (second throttled), got %d", n)
	}
}

func TestForceNewIPBypassesInterval(t *testing.T) {
	srv, rotates := newMockServer(nil, "1.2.3.4")
	defer srv.Close()

	host, port := splitAddr(srv.URL)
	c := NewController(host, port, time.Hour)

	if err := c.ForceNewIP(); err != nil {
		t.Fatalf("first ForceNewIP failed: %v", err)
	}
	if err := c.ForceNewIP(); err != nil {
		t.Fatalf("second ForceNewIP failed: %v", err)
	}
	// ForceNewIP ignores the interval.
	if n := atomic.LoadInt32(rotates); n != 2 {
		t.Fatalf("expected 2 rotate calls, got %d", n)
	}
}

func TestNewIPPropagatesSupervisorError(t *testing.T) {
	srv, _ := newMockServer(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}, "")
	defer srv.Close()

	host, port := splitAddr(srv.URL)
	c := NewController(host, port, time.Hour)

	if err := c.NewIP(); err == nil {
		t.Fatal("expected error when supervisor returns 500")
	}
}

func TestCurrentIPInitiallyEmpty(t *testing.T) {
	c := NewController("127.0.0.1", 1, time.Hour)
	if got := c.CurrentIP(); got != "" {
		t.Fatalf("expected empty CurrentIP, got %q", got)
	}
}
