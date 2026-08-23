package supervisor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/davegallant/vpngate/pkg/vpn"
)

// testPingEnv points the live connectivity check at local servers so unit
// tests run without network access.
func testPingEnv(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("203.0.113.7"))
	}))
	oldDNS := pingDNSHost
	oldEgress := pingEgressURL
	pingDNSHost = "127.0.0.1" // local DNS lookup is fast and offline
	pingEgressURL = []string{srv.URL}
	return srv, func() {
		srv.Close()
		pingDNSHost = oldDNS
		pingEgressURL = oldEgress
	}
}

func TestPingDisconnected(t *testing.T) {
	s := &Supervisor{}
	res := s.Ping()
	if res.Connected {
		t.Fatal("expected disconnected ping")
	}
	if res.DNSOK || res.EgressOK {
		t.Fatal("disconnected ping must not report healthy checks")
	}
	if res.DNSError == "" || res.EgressErr == "" {
		t.Fatalf("expected disconnect errors, got dns=%q egress=%q", res.DNSError, res.EgressErr)
	}
}

func TestPingConnected(t *testing.T) {
	_, restore := testPingEnv(t)
	defer restore()

	s := &Supervisor{
		cur:       fakeProcess(),
		current:   &vpn.Server{HostName: "vpn-test", CountryLong: "South Korea"},
		ip:        "1.2.3.4",
		connected: true,
	}

	res := s.Ping()
	if !res.Connected {
		t.Fatal("expected connected ping")
	}
	if res.Server != "vpn-test" || res.Country != "South Korea" {
		t.Errorf("unexpected server info: %+v", res)
	}
	if !res.DNSOK {
		t.Errorf("expected DNS ok, got error %q", res.DNSError)
	}
	if !res.EgressOK {
		t.Errorf("expected egress ok, got error %q", res.EgressErr)
	}
	if res.EgressIP != "203.0.113.7" {
		t.Errorf("EgressIP = %q, want 203.0.113.7", res.EgressIP)
	}
}

func TestPingEgressFallsBackToSecondEndpoint(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer dead.Close()
	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("203.0.113.9"))
	}))
	defer alive.Close()

	oldEgress := pingEgressURL
	oldDNS := pingDNSHost
	pingEgressURL = []string{dead.URL, alive.URL}
	pingDNSHost = "127.0.0.1"
	defer func() {
		pingEgressURL = oldEgress
		pingDNSHost = oldDNS
	}()

	s := &Supervisor{cur: fakeProcess(), ip: "", connected: true}

	res := s.Ping()
	if !res.EgressOK {
		t.Fatalf("expected egress to succeed via fallback, got %q", res.EgressErr)
	}
	if res.EgressIP != "203.0.113.9" {
		t.Errorf("EgressIP = %q, want 203.0.113.9 (fallback endpoint)", res.EgressIP)
	}
}

func TestPingAllEgressFail(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer dead.Close()

	oldEgress := pingEgressURL
	oldDNS := pingDNSHost
	pingEgressURL = []string{dead.URL}
	pingDNSHost = "127.0.0.1"
	defer func() {
		pingEgressURL = oldEgress
		pingDNSHost = oldDNS
	}()

	s := &Supervisor{cur: fakeProcess(), ip: "", connected: true}

	res := s.Ping()
	if res.EgressOK {
		t.Fatal("expected egress to fail when all endpoints fail")
	}
	if want := "status 503"; res.EgressErr != want {
		t.Errorf("expected last error %q in EgressErr, got %q", want, res.EgressErr)
	}
}

// fakeProcess builds a managedProcess whose cmd looks alive for state
// checks; nothing is ever started or waited on.
func fakeProcess() *managedProcess {
	return &managedProcess{
		cmd:  &exec.Cmd{Process: &os.Process{Pid: 999999}},
		done: make(chan struct{}),
	}
}

func TestNewDirectFallbackWhenOpenVPNMissing(t *testing.T) {
	orig := lookPath
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	defer func() { lookPath = orig }()

	s := New(Config{SocksAddr: "127.0.0.1:9050"})
	if !s.IsDirect() {
		t.Fatal("expected direct mode when openvpn is missing")
	}
	if got := s.CurrentIP(); got != "direct" {
		t.Fatalf("CurrentIP = %q, want direct", got)
	}
	if s.InstallHint() == "" {
		t.Fatal("expected a non-empty install hint")
	}
	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start in direct mode must succeed, got %v", err)
	}
}

func TestRotateBeforeStartWithCancelledCtx(t *testing.T) {
	// Regression: Rotate used to dereference a nil ctx before Start and
	// panic. With a cancelled ctx it must return an error instead.
	s := New(Config{SocksAddr: "127.0.0.1:19052"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ctx, s.cancel = ctx, cancel

	err := s.Rotate()
	if err == nil {
		t.Fatal("expected error rotating after shutdown")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSleepCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(ctx, time.Hour) {
		t.Fatal("sleepCtx must report false when ctx is already cancelled")
	}
	if !sleepCtx(context.Background(), time.Millisecond) {
		t.Fatal("sleepCtx must report true when the duration elapsed")
	}
}
