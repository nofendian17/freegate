package supervisor

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchPublicIPFallsBackToSecondEndpoint(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer dead.Close()
	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("203.0.113.7"))
	}))
	defer alive.Close()

	oldProbes := ipEchoProbes
	ipEchoProbes = []ipEchoProbe{
		{url: dead.URL},
		{url: alive.URL},
	}
	defer func() { ipEchoProbes = oldProbes }()

	ip, err := fetchPublicIP()
	if err != nil {
		t.Fatalf("expected success via fallback, got %v", err)
	}
	if ip != "203.0.113.7" {
		t.Errorf("ip = %q, want 203.0.113.7", ip)
	}
}

func TestFetchPublicIPJSONEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ip":"200.1.2.3"}`))
	}))
	defer srv.Close()

	oldProbes := ipEchoProbes
	ipEchoProbes = []ipEchoProbe{{url: srv.URL, jsonKey: "ip"}}
	defer func() { ipEchoProbes = oldProbes }()

	ip, err := fetchPublicIP()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip != "200.1.2.3" {
		t.Errorf("ip = %q, want 200.1.2.3", ip)
	}
}

func TestFetchPublicIPAllProbesFail(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer dead.Close()

	oldProbes := ipEchoProbes
	ipEchoProbes = []ipEchoProbe{{url: dead.URL}}
	defer func() { ipEchoProbes = oldProbes }()

	if ip, err := fetchPublicIP(); err == nil || ip != "" {
		t.Fatalf("expected error with empty ip, got ip=%q err=%v", ip, err)
	}
}
