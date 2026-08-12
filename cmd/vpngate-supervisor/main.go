// Command vpngate-supervisor runs inside the freegate "vpn" sidecar
// container. It keeps an OpenVPN tunnel to a VPNGate relay server, exposes
// a SOCKS5 proxy through that tunnel, and a small HTTP control API that the
// freegate proxy uses to rotate the exit IP.
//
// Environment:
//
//	VPNGATE_SOCKS_PORT      SOCKS5 listen port (default 9050)
//	VPNGATE_CTRL_PORT       control API listen port (default 8080)
//	VPNGATE_COUNTRY         optional country filter (name or ISO code;
//	                        prefix with ! to exclude, e.g. !Japan)
//	VPNGATE_MIN_SCORE       optional minimum server score
//	VPNGATE_MAX_PING        optional maximum ping (ms)
//	VPNGATE_REFRESH_SECONDS how often to refresh the server list (default 300)
//	VPNGATE_LOG_LEVEL       slog level (default info)
//
// Security note: VPNGate servers are community-run and NOT trusted. Upstream
// calls are HTTPS end-to-end so API keys stay protected, but the relay sees
// destination metadata. This provides IP rotation, not anonymity.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/armon/go-socks5"
	"github.com/davegallant/vpngate/pkg/vpn"
)

// errRotationInProgress is returned by rotate when another rotation (e.g.
// from the reconnect loop) is already running.
var errRotationInProgress = errors.New("rotation already in progress")

// errServerNotFound is returned by connectToServerByName when the requested
// hostname is not in the (filtered) server list.
var errServerNotFound = errors.New("server not found in list")

// pingProbeTargets are the hosts/URLs the /ping connectivity check probes
// through the tunnel. They are package-level vars so tests can point them
// at local servers instead of the live internet.
var (
	pingDNSHost   = "opencode.ai"
	pingEgressURL = []string{"https://api.ipify.org", "https://ifconfig.me/ip"}
)

const (
	// Server-selection weighting. Score is the primary reliability signal;
	// ping is secondary (many relays report 0 or no ping at all).
	selectionScoreWeight = 0.7
	selectionPingWeight  = 0.3
	// selectionMinWeight keeps every filtered candidate eligible even when
	// its score is far below the best one, preserving exit-IP variety.
	selectionMinWeight = 0.05

	// rotateAttempts is how many different servers a single rotation tries
	// before giving up (free VPNGate relays are often dead or full).
	rotateAttempts = 3
	// tunnelWaitTimeout bounds how long a single openvpn attempt may take
	// to bring tun0 up.
	tunnelWaitTimeout = 8 * time.Second
	// ipCheckTimeout bounds the best-effort egress check after tun0 is up.
	// A slow-but-usable relay is not rejected here; the ipRefresher reaps
	// tunnels that stop routing.
	ipCheckTimeout = 6 * time.Second
	// ipRefreshAttempts / ipRefreshRetryDelay give the background IP
	// refresher patience with slow free relays instead of giving up after
	// one timed-out probe per tick.
	ipRefreshAttempts     = 3
	ipRefreshRetryDelay   = 3 * time.Second
	// listFetchTimeout bounds a single server-list refresh so a hung
	// fetch cannot wedge the rotation state.
	listFetchTimeout = 20 * time.Second
)

func main() {
	if os.Getenv("VPNGATE_LOG_LEVEL") == "debug" {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}
	slog.Info("vpngate: supervisor starting", "debug", os.Getenv("VPNGATE_LOG_LEVEL"))

	s := &supervisor{
		socksAddr:  "0.0.0.0:" + envStr("VPNGATE_SOCKS_PORT", "9050"),
		ctrlAddr:   "0.0.0.0:" + envStr("VPNGATE_CTRL_PORT", "8080"),
		country:    envStr("VPNGATE_COUNTRY", ""),
		minScore:   envInt("VPNGATE_MIN_SCORE", 0),
		maxPing:    envInt("VPNGATE_MAX_PING", 0),
		refreshInt: time.Duration(envInt("VPNGATE_REFRESH_SECONDS", 300)) * time.Second,
	}

	go s.reconnectLoop()
	go s.ipRefresher()

	go func() {
		if err := s.serveSOCKS(); err != nil {
			slog.Error("vpngate: socks server exited", "error", err)
		}
	}()

	srv := &http.Server{Addr: s.ctrlAddr, Handler: s.routes()}
	go func() {
		slog.Info("vpngate: control API listening", "addr", s.ctrlAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("vpngate: control API exited", "error", err)
		}
	}()

	// Block until told to stop, then shut down the tunnel.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	<-ch
	slog.Info("vpngate: shutting down")
	s.shutdown()
}

// supervisor owns the tunnel lifecycle and the current connection state.
type supervisor struct {
	socksAddr string
	ctrlAddr  string

	country    string
	minScore   int
	maxPing    int
	refreshInt time.Duration

	mu          sync.Mutex
	rotating    bool
	cmd         *exec.Cmd
	cfgPath     string
	current     *vpn.Server
	ip          string
	connected   bool
	connectedAt time.Time
	servers     []vpn.Server
	lastRefresh time.Time
}

// reconnectLoop keeps trying until a tunnel is up, and reconnects
// whenever the openvpn process exits unexpectedly. While disconnected it
// retries aggressively (2s) to minimize dead-tunnel windows. When another
// rotation is already running (e.g. a request-triggered /rotate), it
// waits silently instead of logging or racing it.
func (s *supervisor) reconnectLoop() {
	for {
		if s.isConnected() {
			time.Sleep(5 * time.Second)
			continue
		}
		if s.isRotating() {
			time.Sleep(2 * time.Second)
			continue
		}
		slog.Info("vpngate: connecting to a vpn server")
		if err := s.rotate(); err != nil {
			if !errors.Is(err, errRotationInProgress) {
				slog.Warn("vpngate: connect failed", "error", err)
			}
			time.Sleep(2 * time.Second)
			continue
		}
		slog.Info("vpngate: connected", "server", s.serverName(), "ip", s.currentIP())
		time.Sleep(5 * time.Second)
	}
}

// rotate tears down the current tunnel (if any) and connects to a
// different server. Free VPNGate relays are flaky, so it tries up to
// rotateAttempts servers before giving up. It blocks until a tunnel is up
// or all attempts fail.
//
// Only one rotation runs at a time: concurrent callers (e.g. the reconnect
// loop racing an explicit /rotate) are no-ops. The mutex is only held for
// short state reads/writes, never while a tunnel is being brought up, so
// the control API stays responsive during a slow rotation.
func (s *supervisor) rotate() error {
	end, err := s.beginRotation()
	if err != nil {
		return err
	}
	defer end()

	success := false
	defer func() {
		if !success {
			s.mu.Lock()
			s.connected = false
			s.mu.Unlock()
		}
	}()

	// Build the tried set from the current connection.
	s.mu.Lock()
	tried := map[string]bool{}
	if s.current != nil {
		tried[s.current.HostName] = true
	}
	s.mu.Unlock()

	// Pick up to rotateAttempts candidates up-front: getServers may fetch
	// the list over the network.
	candidates := make([]vpn.Server, 0, rotateAttempts)
	for len(candidates) < rotateAttempts {
		server, err := s.pickServer(tried)
		if err != nil {
			break
		}
		tried[server.HostName] = true
		candidates = append(candidates, server)
	}
	if len(candidates) == 0 {
		return fmt.Errorf("rotation failed: no servers matched the filters")
	}

	var lastErr error
	for _, server := range candidates {
		if err := s.connectToServer(server); err != nil {
			lastErr = err
			slog.Warn("vpngate: connect attempt failed, trying another server", "server", server.HostName, "error", err)
			continue
		}
		success = true
		return nil
	}
	return fmt.Errorf("rotation failed after %d attempt(s): %w", len(candidates), lastErr)
}

// beginRotation acquires the single-rotation guard, or returns
// errRotationInProgress if another rotation is already running. The
// returned func releases the guard; call it via defer.
func (s *supervisor) beginRotation() (func(), error) {
	s.mu.Lock()
	if s.rotating {
		s.mu.Unlock()
		return nil, errRotationInProgress
	}
	s.rotating = true
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.rotating = false
		s.mu.Unlock()
	}, nil
}

// connectToServer tears down the current tunnel (if any) and brings up a
// new one to the given server. The caller must hold the rotation guard.
// The mutex is only held for short state reads/writes, never while the
// tunnel is being killed or brought up, so the control API stays
// responsive during a slow connect.
func (s *supervisor) connectToServer(server vpn.Server) error {
	// Tear down the previous tunnel outside the lock so the control
	// API stays responsive while the process is being killed.
	s.mu.Lock()
	old, oldPath := s.cmd, s.cfgPath
	s.cmd = nil
	s.connected = false
	s.cfgPath = ""
	s.mu.Unlock()
	if old != nil {
		killProcess(old)
	}
	if oldPath != "" {
		os.Remove(oldPath)
	}

	waitTunDown()

	cmd, cfgPath, err := startOpenVPN(server)
	if err != nil {
		return err
	}
	go s.watch(cmd)

	ip, err := waitTunnelUp(cmd)
	if err != nil {
		killProcess(cmd)
		return err
	}

	s.mu.Lock()
	s.cmd = cmd
	s.cfgPath = cfgPath
	s.current = &server
	if ip == "" {
		// The measured egress IP is often blank on slow relays: the
		// check right after tun0 is up is best-effort and frequently
		// times out. The relay's own address is a reliable stand-in —
		// traffic exits at the relay — so /ip and the dashboard label
		// stay populated instead of showing "—" until the refresher
		// refines the value.
		ip = server.IPAddr
	}
	s.ip = ip
	s.connected = true
	s.connectedAt = time.Now()
	s.mu.Unlock()
	return nil
}

// connectToServerByName resolves a hostname from the cached server list and
// connects to it, holding the rotation guard. This is the explicit
// user-driven path (/connect). Only servers that pass the configured
// filters are connectable, so the picker and the direct API agree on what
// the operator is allowed to select.
func (s *supervisor) connectToServerByName(hostname string) error {
	servers, err := s.getServers()
	if err != nil {
		return fmt.Errorf("fetch server list: %w", err)
	}
	for _, sv := range servers {
		if sv.HostName == hostname && s.serverMatchesFilters(sv) {
			end, err := s.beginRotation()
			if err != nil {
				return err
			}
			defer end()
			if err := s.connectToServer(sv); err != nil {
				s.mu.Lock()
				s.connected = false
				s.mu.Unlock()
				return err
			}
			return nil
		}
	}
	return errServerNotFound
}

// watch waits for an openvpn process and marks the tunnel disconnected
// if it was still the active one.
func (s *supervisor) watch(cmd *exec.Cmd) {
	err := cmd.Wait()
	s.mu.Lock()
	isCurrent := s.cmd == cmd
	cfgPath := ""
	if isCurrent {
		cfgPath = s.cfgPath
		s.cmd = nil
		s.cfgPath = ""
		s.connected = false
	}
	s.mu.Unlock()
	if cfgPath != "" {
		os.Remove(cfgPath)
	}
	if !isCurrent {
		return // a newer process replaced this one
	}
	if err != nil {
		slog.Warn("vpngate: openvpn exited", "error", err)
	} else {
		slog.Info("vpngate: openvpn exited")
	}
}

// serverMatchesFilters reports whether a server passes the configured
// country / score / ping filters.
func (s *supervisor) serverMatchesFilters(sv vpn.Server) bool {
	if !matchCountry(s.country, sv) {
		return false
	}
	if s.minScore > 0 && sv.Score < s.minScore {
		return false
	}
	if s.maxPing > 0 {
		if p, ok := parsePing(sv.Ping); !ok || p > s.maxPing {
			return false
		}
	}
	return true
}

// pickServer returns a random server matching the filters, excluding any
// hostnames in tried. Selection is ping-aware weighted random: every
// candidate that passes the filters stays eligible, but relays with
// higher scores and lower pings are picked more often. (Picking only the
// top-scoring relays favoured the busiest — and most flaky — ones.)
func (s *supervisor) pickServer(tried map[string]bool) (vpn.Server, error) {
	servers, err := s.getServers()
	if err != nil {
		return vpn.Server{}, err
	}
	var candidates []vpn.Server
	for _, sv := range servers {
		if tried[sv.HostName] {
			continue
		}
		if !s.serverMatchesFilters(sv) {
			continue
		}
		candidates = append(candidates, sv)
	}
	if len(candidates) == 0 {
		return vpn.Server{}, fmt.Errorf("no vpngate servers match the filters")
	}
	return pickWeighted(candidates), nil
}

// pickWeighted chooses a server via weighted random selection. Weights
// combine score and ping, each normalized across the candidate set: a
// higher score or a lower ping raises the odds, but every candidate stays
// eligible so consecutive rotations still vary the exit IP.
func pickWeighted(candidates []vpn.Server) vpn.Server {
	maxScore, maxPing := 0, 0
	for _, sv := range candidates {
		if sv.Score > maxScore {
			maxScore = sv.Score
		}
		if p, ok := parsePing(sv.Ping); ok && p > maxPing {
			maxPing = p
		}
	}

	weights := make([]float64, len(candidates))
	var total float64
	for i, sv := range candidates {
		w := selectionWeight(sv, maxScore, maxPing)
		weights[i] = w
		total += w
	}

	r := rand.Float64() * total
	for i, w := range weights {
		r -= w
		if r <= 0 {
			return candidates[i]
		}
	}
	return candidates[len(candidates)-1]
}

// selectionWeight computes the weight for one candidate given the max
// score and max ping across the candidate set. A higher score or a lower
// ping raises the weight; ping 0 or missing means "unknown" and gets a
// neutral weight, never the bonus a real low ping would earn.
func selectionWeight(sv vpn.Server, maxScore, maxPing int) float64 {
	scoreW := 1.0
	if maxScore > 0 {
		scoreW = float64(sv.Score) / float64(maxScore)
	}
	pingW := 0.5
	if p, ok := parsePing(sv.Ping); ok && p > 0 && maxPing > 0 {
		pingW = 1 - float64(p)/float64(maxPing)
	}
	w := selectionScoreWeight*scoreW + selectionPingWeight*pingW
	if w < selectionMinWeight {
		w = selectionMinWeight
	}
	return w
}

// getServers returns the cached server list, refreshing it (via the
// vpngate library) when stale.
func (s *supervisor) getServers() ([]vpn.Server, error) {
	s.mu.Lock()
	refresh := s.lastRefresh.IsZero() || time.Since(s.lastRefresh) >= s.refreshInt
	servers := s.servers
	s.mu.Unlock()

	if refresh || len(servers) == 0 {
		list, err := fetchServerList(refresh)
		if err != nil {
			if len(servers) == 0 {
				return nil, fmt.Errorf("fetch vpngate server list: %w", err)
			}
			// Keep serving from the stale cache instead of wedging.
			slog.Warn("vpngate: server list refresh failed, using stale cache", "error", err)
		} else {
			servers = *list
			s.mu.Lock()
			s.servers = servers
			s.lastRefresh = time.Now()
			s.mu.Unlock()
		}
	}
	return servers, nil
}

// fetchServerList wraps vpn.GetListWithOptions with a hard timeout so a
// hung upstream (the library retries internally for up to minutes) cannot
// stall a rotation.
func fetchServerList(refresh bool) (*[]vpn.Server, error) {
	type result struct {
		servers *[]vpn.Server
		err     error
	}
	ch := make(chan result, 1)
	go func() {
		list, err := vpn.GetListWithOptions("", "", vpn.ListOptions{Refresh: refresh})
		ch <- result{servers: list, err: err}
	}()
	select {
	case r := <-ch:
		return r.servers, r.err
	case <-time.After(listFetchTimeout):
		return nil, fmt.Errorf("server list fetch timed out after %s", listFetchTimeout)
	}
}

func (s *supervisor) isConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected && s.cmd != nil
}

func (s *supervisor) isRotating() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotating
}

func (s *supervisor) serverName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return ""
	}
	return s.current.HostName
}

func (s *supervisor) currentIP() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ip
}

// startOpenVPN decodes the server's base64 OpenVPN config and launches
// the openvpn binary with it.
func startOpenVPN(server vpn.Server) (*exec.Cmd, string, error) {
	decoded, err := base64.StdEncoding.DecodeString(server.OpenVpnConfigData)
	if err != nil {
		return nil, "", fmt.Errorf("decode openvpn config: %w", err)
	}
	tmp, err := os.CreateTemp("", "fg-vpngate-*.ovpn")
	if err != nil {
		return nil, "", fmt.Errorf("create config file: %w", err)
	}
	if _, err := tmp.Write(decoded); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, "", fmt.Errorf("write config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return nil, "", err
	}

	cmd := exec.Command("openvpn",
		"--verb", "3",
		"--config", tmp.Name(),
		"--data-ciphers", "AES-128-CBC",
		// Keep the container's DNS (Docker embedded DNS) instead of letting
		// the pushed config rewrite /etc/resolv.conf. Note: --pull-filter
		// takes exactly one pattern argument, so "dhcp-option" must be a
		// single token (not "dhcp-option DNS").
		"--pull-filter", "ignore", "dhcp-option",
		// Bound openvpn's own reconnect attempts to a dead relay so it
		// exits (instead of retrying the same server forever) and the
		// reconnect loop can pick a different one.
		"--connect-retry", "2",
		"--connect-retry-max", "5",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		os.Remove(tmp.Name())
		return nil, "", fmt.Errorf("start openvpn: %w", err)
	}
	return cmd, tmp.Name(), nil
}

// killProcess terminates an openvpn process gracefully, then forcefully.
func killProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // gone
		}
		time.Sleep(150 * time.Millisecond)
	}
	slog.Warn("vpngate: openvpn did not exit after SIGTERM, killing", "pid", pid)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// waitTunDown waits for the old tun0 interface to disappear so the next
// openvpn instance can reuse it.
func waitTunDown() {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.InterfaceByName("tun0"); err != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	slog.Warn("vpngate: tun0 still present after 5s, continuing")
}

// waitTunnelUp blocks until tun0 is up, then does a short best-effort
// egress check. It fails fast if the openvpn process exits (e.g.
// AUTH_FAILED or a dead relay) instead of waiting out the full timeout.
// A slow-but-usable tunnel is still accepted; the ipRefresher reaps
// tunnels that stop routing entirely.
func waitTunnelUp(cmd *exec.Cmd) (string, error) {
	deadline := time.Now().Add(tunnelWaitTimeout)
	for {
		if iface, err := net.InterfaceByName("tun0"); err == nil && iface.Flags&net.FlagUp != 0 {
			break
		}
		if cmd != nil && cmd.Process != nil {
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				return "", fmt.Errorf("openvpn exited before tun0 was up")
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("openvpn tunnel (tun0) did not come up within %s", tunnelWaitTimeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Best-effort IP capture; not a health gate.
	ipDeadline := time.Now().Add(ipCheckTimeout)
	for {
		if ip, err := fetchPublicIP(); err == nil && ip != "" {
			return ip, nil
		}
		if time.Now().After(ipDeadline) {
			return "", nil
		}
		time.Sleep(750 * time.Millisecond)
	}
}

// ipRefresher periodically re-checks the tunnel's public IP so /ip and the
// dashboard stay accurate even when the check right after connecting fails
// (routing/DNS not settled yet). It deliberately does NOT kill the tunnel:
// a slow egress check used to recycle healthy relays every ~40s. Dead
// tunnels are detected by openvpn itself (ping-restart / connect-retry-max)
// and replaced by the reconnect loop when the process exits.
func (s *supervisor) ipRefresher() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		connected := s.connected && s.cmd != nil
		s.mu.Unlock()
		if !connected {
			continue
		}
		// Egress probes through free relays routinely need a few tries
		// before any echo service answers. Retry inside the tick so a
		// slow-but-working tunnel fills the label in this cycle instead
		// of waiting for the next tick.
		for attempt := 0; attempt < ipRefreshAttempts; attempt++ {
			ip, err := fetchPublicIP()
			if err == nil && ip != "" {
				s.mu.Lock()
				if s.connected && s.cmd != nil {
					s.ip = ip
				}
				s.mu.Unlock()
				break
			}
			slog.Debug("vpngate: ip refresh attempt failed",
				"attempt", attempt+1, "error", err)
			if attempt+1 < ipRefreshAttempts {
				time.Sleep(ipRefreshRetryDelay)
			}
		}
	}
}

// pingResult is the outcome of a live connectivity check through the
// tunnel (POST /ping).
type pingResult struct {
	Connected bool   `json:"connected"`
	Server    string `json:"server"`
	Country   string `json:"country"`
	IP        string `json:"ip"`

	DNSOK    bool   `json:"dns_ok"`
	DNSMS    int64  `json:"dns_ms"`
	DNSError string `json:"dns_error,omitempty"`

	EgressOK bool   `json:"egress_ok"`
	EgressIP string `json:"egress_ip,omitempty"`
	HTTPMS   int64  `json:"http_ms"`
	HTTPCode int    `json:"http_code"`
	EgressErr string `json:"egress_error,omitempty"`
}

// ping performs a live connectivity check through the tunnel: it resolves
// a hostname, then does an HTTPS GET to a public IP echo service measuring
// round-trip latency. This answers "is the VPN actually routing traffic?"
// beyond the tunnel being up — a tunnel can be connected while the relay
// or the route is dead.
func (s *supervisor) ping() pingResult {
	res := pingResult{}
	s.mu.Lock()
	res.Connected = s.connected && s.cmd != nil
	if s.current != nil {
		res.Server = s.current.HostName
		res.Country = s.current.CountryLong
	}
	res.IP = s.ip
	s.mu.Unlock()

	if !res.Connected {
		res.DNSError = "tunnel not connected"
		res.EgressErr = "tunnel not connected"
		return res
	}

	// DNS resolution through the tunnel (Docker DNS + relay routing).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	dnsStart := time.Now()
	_, err := net.DefaultResolver.LookupHost(ctx, pingDNSHost)
	dnsMS := time.Since(dnsStart).Milliseconds()
	cancel()
	res.DNSMS = dnsMS
	if err != nil {
		res.DNSError = err.Error()
	} else {
		res.DNSOK = true
	}

	// HTTPS egress probe through the tunnel with latency measurement.
	// Tries the endpoint list in order so a single slow/down service
	// (common through flaky free relays) does not report the tunnel as
	// broken.
	client := &http.Client{Timeout: 12 * time.Second}
	var lastErr string
	for _, u := range pingEgressURL {
		httpStart := time.Now()
		resp, err := client.Get(u)
		httpMS := time.Since(httpStart).Milliseconds()
		res.HTTPMS = httpMS
		if err != nil {
			lastErr = err.Error()
			continue
		}
		res.HTTPCode = resp.StatusCode
		if resp.StatusCode == http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
			resp.Body.Close()
			if ip := strings.TrimSpace(string(body)); ip != "" {
				res.EgressOK = true
				res.EgressIP = ip
				return res
			}
		}
		resp.Body.Close()
		lastErr = fmt.Sprintf("status %d", resp.StatusCode)
	}
	res.EgressErr = lastErr
	if res.EgressErr == "" {
		res.EgressErr = "no egress endpoint reachable"
	}
	return res
}

// ipEchoProbe describes a public service that answers with the caller's
// public IP. jsonKey selects a field in a JSON response; empty means the
// body is the bare IP.
type ipEchoProbe struct {
	url     string
	jsonKey string
}

// ipEchoProbes are tried in order by fetchPublicIP.
var ipEchoProbes = []ipEchoProbe{
	{url: "https://api.ipify.org?format=json", jsonKey: "ip"},
	{url: "https://ifconfig.me/ip"},
}

// probeIP fetches one echo endpoint and returns the public IP it reports.
// The timeout is per endpoint so one slow or blackholed service cannot eat
// the whole attempt.
func probeIP(p ipEchoProbe) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(p.url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: status %d", p.url, resp.StatusCode)
	}
	if p.jsonKey != "" {
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			return "", fmt.Errorf("%s: decode: %w", p.url, err)
		}
		ip := strings.TrimSpace(fmt.Sprint(out[p.jsonKey]))
		if ip == "" {
			return "", fmt.Errorf("%s: empty ip field", p.url)
		}
		return ip, nil
	}
	ip := strings.TrimSpace(string(body))
	if ip == "" {
		return "", fmt.Errorf("%s: empty body", p.url)
	}
	return ip, nil
}

// fetchPublicIP returns the public IP as seen through the tunnel, trying
// the echo endpoints in order. Free VPN relays are slow, so failed
// endpoints are skipped rather than aborted.
func fetchPublicIP() (string, error) {
	var lastErr error
	for _, p := range ipEchoProbes {
		ip, err := probeIP(p)
		if err == nil && ip != "" {
			return ip, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no ip echo endpoint reachable")
	}
	return "", lastErr
}

// serveSOCKS runs the SOCKS5 proxy that freegate's upstream client dials.
// The dial timeout bounds how long a client connection waits when the
// tunnel is down (fail fast instead of hanging); it does not bound the
// lifetime of established streams.
func (s *supervisor) serveSOCKS() error {
	conf := &socks5.Config{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, addr)
		},
	}
	server, err := socks5.New(conf)
	if err != nil {
		return fmt.Errorf("create socks5 server: %w", err)
	}
	slog.Info("vpngate: socks5 listening", "addr", s.socksAddr)
	return server.ListenAndServe("tcp", s.socksAddr)
}

// routes wires the control API used by the freegate proxy.
func (s *supervisor) routes() http.Handler {
	mux := http.NewServeMux()

	// POST /ping runs a live connectivity check through the tunnel: DNS
	// resolution, an HTTPS egress probe (with latency), and the current
	// tunnel state. Used by the dashboard's "ping" button.
	mux.HandleFunc("POST /ping", func(w http.ResponseWriter, r *http.Request) {
		res := s.ping()
		writeJSON(w, http.StatusOK, res)
	})

	mux.HandleFunc("POST /rotate", func(w http.ResponseWriter, r *http.Request) {
		if err := s.rotate(); err != nil {
			if errors.Is(err, errRotationInProgress) {
				writeErr(w, http.StatusConflict, err)
				return
			}
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.mu.Lock()
		ip := s.ip
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ip": ip})
	})

	// GET /servers lists the currently available servers (after filters)
	// so the dashboard can render a picker.
	mux.HandleFunc("GET /servers", func(w http.ResponseWriter, r *http.Request) {
		servers, err := s.getServers()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out := make([]map[string]any, 0, len(servers))
		for _, sv := range servers {
			if !s.serverMatchesFilters(sv) {
				continue
			}
			out = append(out, map[string]any{
				"hostname":     sv.HostName,
				"ip":           sv.IPAddr,
				"country":      sv.CountryLong,
				"country_code": sv.CountryShort,
				"score":        sv.Score,
				"ping":         sv.Ping,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"servers": out})
	})

	// POST /connect connects the tunnel to a specific server chosen by the
	// user: {"server": "<hostname>"}. Returns 404 if the hostname is not
	// in the current list.
	mux.HandleFunc("POST /connect", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Server string `json:"server"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Server == "" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("body must be {\"server\": \"<hostname>\"}"))
			return
		}
		if err := s.connectToServerByName(req.Server); err != nil {
			switch {
			case errors.Is(err, errRotationInProgress):
				writeErr(w, http.StatusConflict, err)
			case errors.Is(err, errServerNotFound):
				writeErr(w, http.StatusNotFound, err)
			default:
				writeErr(w, http.StatusInternalServerError, err)
			}
			return
		}
		s.mu.Lock()
		ip := s.ip
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ip": ip})
	})

	mux.HandleFunc("GET /ip", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		ip := s.ip
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ip": ip})
	})

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		status := map[string]any{
			"connected":    s.connected,
			"server":       s.serverNameUnlocked(),
			"country":      s.serverCountryUnlocked(),
			"ip":           s.ip,
			"connected_at": s.connectedAt.Unix(),
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, status)
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		// A rotation in progress counts as healthy: it either succeeds or
		// the reconnect loop keeps retrying until it does.
		ok := s.rotating || (s.connected && s.cmd != nil)
		s.mu.Unlock()
		if !ok {
			http.Error(w, "not connected", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	return mux
}

func (s *supervisor) serverNameUnlocked() string {
	if s.current == nil {
		return ""
	}
	return s.current.HostName
}

func (s *supervisor) serverCountryUnlocked() string {
	if s.current == nil {
		return ""
	}
	return s.current.CountryLong
}

func (s *supervisor) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		killProcess(s.cmd)
		s.cmd = nil
	}
	if s.cfgPath != "" {
		os.Remove(s.cfgPath)
		s.cfgPath = ""
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	slog.Error("vpngate: request failed", "error", err)
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

// matchCountry reports whether a server matches the VPNGATE_COUNTRY
// filter. A leading "!" turns the filter into an exclusion, e.g.
// "!Japan" matches every country except Japan.
func matchCountry(filter string, s vpn.Server) bool {
	if filter == "" {
		return true
	}
	f := strings.ToLower(strings.TrimSpace(filter))
	if strings.HasPrefix(f, "!") {
		excl := strings.TrimSpace(f[1:])
		if excl == "" {
			return true
		}
		return !strings.Contains(strings.ToLower(s.CountryLong), excl) &&
			!strings.EqualFold(s.CountryShort, excl)
	}
	return strings.Contains(strings.ToLower(s.CountryLong), f) ||
		strings.EqualFold(s.CountryShort, f)
}

func parsePing(ping string) (int, bool) {
	if ping == "" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(ping))
	return n, err == nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
