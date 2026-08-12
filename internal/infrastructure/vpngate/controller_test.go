package vpngate

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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

func TestListServers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/servers" {
			http.NotFound(w, r)
			return
		}
		writeTestJSON(w, map[string]any{"servers": []ServerInfo{
			{Hostname: "vpn-korea-1", IP: "1.2.3.4", Country: "South Korea", CountryCode: "KR", Score: 5000, Ping: "12"},
			{Hostname: "vpn-th-1", IP: "5.6.7.8", Country: "Thailand", CountryCode: "TH", Score: 3000, Ping: "80"},
		}})
	}))
	defer srv.Close()

	host, port := splitAddr(srv.URL)
	c := NewController(host, port, time.Hour)

	servers, err := c.ListServers()
	if err != nil {
		t.Fatalf("ListServers failed: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
	if servers[0].Hostname != "vpn-korea-1" || servers[0].Country != "South Korea" {
		t.Errorf("unexpected server: %+v", servers[0])
	}
}

func TestConnectTo(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connect" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		writeTestJSON(w, map[string]string{"ip": "9.9.9.9"})
	}))
	defer srv.Close()

	host, port := splitAddr(srv.URL)
	c := NewController(host, port, time.Hour)

	if err := c.ConnectTo("vpn-korea-1"); err != nil {
		t.Fatalf("ConnectTo failed: %v", err)
	}
	if !strings.Contains(gotBody, "vpn-korea-1") {
		t.Errorf("expected hostname in request body, got %q", gotBody)
	}
	if got := c.CurrentIP(); got != "9.9.9.9" {
		t.Errorf("CurrentIP = %q, want %q", got, "9.9.9.9")
	}
}

func TestConnectToPropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		writeTestJSON(w, map[string]any{"error": "server dead"})
	}))
	defer srv.Close()

	host, port := splitAddr(srv.URL)
	c := NewController(host, port, time.Hour)

	err := c.ConnectTo("vpn-dead")
	if err == nil {
		t.Fatal("expected error when supervisor rejects connect")
	}
	if !strings.Contains(err.Error(), "server dead") {
		t.Errorf("expected supervisor message in error, got %v", err)
	}
}

func TestStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		writeTestJSON(w, StatusInfo{Connected: true, Server: "vpn-korea-1", Country: "South Korea", IP: "1.2.3.4", ConnectedAt: 12345})
	}))
	defer srv.Close()

	host, port := splitAddr(srv.URL)
	c := NewController(host, port, time.Hour)

	st, err := c.Status()
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !st.Connected || st.Server != "vpn-korea-1" || st.IP != "1.2.3.4" {
		t.Errorf("unexpected status: %+v", st)
	}
}

func TestPing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		writeTestJSON(w, PingResult{
			Connected: true, Server: "vpn-korea-1", IP: "1.2.3.4",
			DNSOK: true, DNSMS: 11, EgressOK: true, EgressIP: "1.2.3.4", HTTPMS: 180, HTTPCode: 200,
		})
	}))
	defer srv.Close()

	host, port := splitAddr(srv.URL)
	c := NewController(host, port, time.Hour)

	res, err := c.Ping()
	if err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
	if !res.Connected || !res.DNSOK || !res.EgressOK {
		t.Errorf("unexpected ping result: %+v", res)
	}
	if res.EgressIP != "1.2.3.4" {
		t.Errorf("EgressIP = %q, want %q", res.EgressIP, "1.2.3.4")
	}
	if got := c.CurrentIP(); got != "1.2.3.4" {
		t.Errorf("CurrentIP = %q, want %q (ping should refresh cached IP)", got, "1.2.3.4")
	}
}

func TestPingPropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	host, port := splitAddr(srv.URL)
	c := NewController(host, port, time.Hour)

	if _, err := c.Ping(); err == nil {
		t.Fatal("expected error when supervisor returns 503")
	}
}

func TestStatusPropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	host, port := splitAddr(srv.URL)
	c := NewController(host, port, time.Hour)

	if _, err := c.Status(); err == nil {
		t.Fatal("expected error when supervisor returns 503")
	}
}
