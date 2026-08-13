package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDialerDefaultTunnel(t *testing.T) {
	d := NewDialer("")
	if d.IsDirect() {
		t.Fatal("expected dialer to default to tunnel (non-direct)")
	}
}

func TestDialerSetDirect(t *testing.T) {
	d := NewDialer("")
	d.SetDirect(true)
	if !d.IsDirect() {
		t.Fatal("expected direct mode after SetDirect(true)")
	}
	d.SetDirect(false)
	if d.IsDirect() {
		t.Fatal("expected tunnel mode after SetDirect(false)")
	}
}

func TestDialerDirectRoutesWithoutSocks(t *testing.T) {
	// A dialer with no socks address can still serve HTTP in direct mode.
	d := NewDialer("")
	d.SetDirect(true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "direct-ok")
	}))
	defer srv.Close()

	hc := NewHTTPClient(srv.URL, []string{"key"}, d, nil)
	resp, err := hc.Get(context.Background(), "/")
	if err != nil {
		t.Fatalf("direct request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "direct-ok" {
		t.Errorf("unexpected body: %q", body)
	}
}

func TestDialerToggle(t *testing.T) {
	// Toggling back and forth must not break subsequent requests.
	d := NewDialer("")
	d.SetDirect(true)
	d.SetDirect(false)
	d.SetDirect(true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	hc := NewHTTPClient(srv.URL, []string{"key"}, d, nil)
	resp, err := hc.Get(context.Background(), "/")
	if err != nil {
		t.Fatalf("request after toggle failed: %v", err)
	}
	resp.Body.Close()
}
