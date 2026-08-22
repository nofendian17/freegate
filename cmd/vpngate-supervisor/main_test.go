package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/davegallant/vpngate/pkg/vpn"
)

func TestPickWeightedBiasesTowardGoodServers(t *testing.T) {
	candidates := []vpn.Server{
		{HostName: "best", Score: 1_000_000, Ping: "10"},
		{HostName: "same-score-slow", Score: 1_000_000, Ping: "900"},
		{HostName: "worst", Score: 100, Ping: "50"},
	}
	best, worst := 0, 0
	for i := 0; i < 2000; i++ {
		switch pickWeighted(candidates).HostName {
		case "best":
			best++
		case "worst":
			worst++
		}
	}
	if best <= worst {
		t.Fatalf("expected the best server to be picked more often than the worst: best=%d worst=%d", best, worst)
	}
	if best == 2000 {
		t.Fatal("selection was not random: the best server was always picked")
	}
}

func TestPickWeightedPingBreaksScoreTie(t *testing.T) {
	// Equal scores, different pings: the low-ping server must win out.
	candidates := []vpn.Server{
		{HostName: "slow", Score: 500_000, Ping: "800"},
		{HostName: "fast", Score: 500_000, Ping: "20"},
	}
	fast, slow := 0, 0
	for i := 0; i < 2000; i++ {
		switch pickWeighted(candidates).HostName {
		case "fast":
			fast++
		case "slow":
			slow++
		}
	}
	if fast <= slow {
		t.Fatalf("expected the low-ping server to be picked more often: fast=%d slow=%d", fast, slow)
	}
}

func TestMatchCountryExclusion(t *testing.T) {
	jp := vpn.Server{CountryLong: "Japan", CountryShort: "JP"}
	kr := vpn.Server{CountryLong: "Korea Republic of", CountryShort: "KR"}

	if matchCountry("!Japan", jp) {
		t.Error("!Japan should exclude Japan")
	}
	if !matchCountry("!Japan", kr) {
		t.Error("!Japan should allow Korea")
	}
	if !matchCountry("", jp) {
		t.Error("empty filter should allow everything")
	}
	if !matchCountry("!", jp) {
		t.Error("bare ! should behave like empty filter")
	}
	if !matchCountry("!Japan", vpn.Server{CountryLong: "United States", CountryShort: "US"}) {
		t.Error("!Japan should allow United States")
	}
}

func TestPickWeightedSingleCandidate(t *testing.T) {
	got := pickWeighted([]vpn.Server{{HostName: "only", Score: 1, Ping: ""}})
	if got.HostName != "only" {
		t.Fatalf("expected the only candidate, got %q", got.HostName)
	}
}

func TestSelectionWeightPingNeutral(t *testing.T) {
	// Ping "" / "0" means "unknown", not "great": it must not earn the
	// full bonus a real low ping would, and must not be penalized like a
	// real high ping. It should sit strictly between the slowest and
	// fastest known-ping candidates.
	slow := selectionWeight(vpn.Server{Score: 100, Ping: "500"}, 100, 500)
	unknown := selectionWeight(vpn.Server{Score: 100, Ping: ""}, 100, 500)
	zero := selectionWeight(vpn.Server{Score: 100, Ping: "0"}, 100, 500)
	fast := selectionWeight(vpn.Server{Score: 100, Ping: "50"}, 100, 500)

	if unknown != zero {
		t.Fatalf("ping missing (%v) and ping 0 (%v) must be treated the same", unknown, zero)
	}
	if !(slow < unknown && unknown < fast) {
		t.Fatalf("expected slow(%v) < unknown(%v) < fast(%v)", slow, unknown, fast)
	}
}

func TestSelectionWeightMinFloor(t *testing.T) {
	// A candidate far below the best keeps a minimum weight so it stays
	// eligible for selection, preserving exit-IP variety.
	w := selectionWeight(vpn.Server{Score: 1, Ping: "999"}, 1_000_000, 1000)
	if w < selectionMinWeight {
		t.Fatalf("expected weight floored at %v, got %v", selectionMinWeight, w)
	}
}

// testPingEnv points the /ping probes at local servers so unit tests run
// without network access.
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
	s := &supervisor{}
	res := s.ping()
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

	s := &supervisor{
		cmd:     &exec.Cmd{Process: &os.Process{Pid: 999999}},
		current: &vpn.Server{HostName: "vpn-test", CountryLong: "South Korea"},
		ip:      "1.2.3.4",
	}
	s.connected = true

	res := s.ping()
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

	s := &supervisor{cmd: &exec.Cmd{Process: &os.Process{Pid: 999999}}, ip: ""}
	s.connected = true

	res := s.ping()
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

	s := &supervisor{cmd: &exec.Cmd{Process: &os.Process{Pid: 999999}}, ip: ""}
	s.connected = true

	res := s.ping()
	if res.EgressOK {
		t.Fatal("expected egress to fail when all endpoints fail")
	}
	if !strings.Contains(res.EgressErr, "status 503") {
		t.Errorf("expected last error in EgressErr, got %q", res.EgressErr)
	}
}

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

