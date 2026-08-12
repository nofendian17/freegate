// Command vpngate-supervisor runs inside the freegate "vpn" sidecar
// container. It keeps an OpenVPN tunnel to a VPNGate relay server, exposes
// a SOCKS5 proxy through that tunnel, and a small HTTP control API that the
// freegate proxy uses to rotate the exit IP.
//
// Environment:
//
//	VPNGATE_SOCKS_PORT      SOCKS5 listen port (default 9050)
//	VPNGATE_CTRL_PORT       control API listen port (default 8080)
//	VPNGATE_COUNTRY         optional country filter (name or ISO code)
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
	// listFetchTimeout bounds a single server-list refresh so a hung
	// fetch cannot wedge the rotation state.
	listFetchTimeout = 20 * time.Second
)

func main() {
	if os.Getenv("VPNGATE_LOG_LEVEL") == "debug" {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}

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
	s.mu.Lock()
	if s.rotating {
		s.mu.Unlock()
		return errRotationInProgress
	}
	s.rotating = true
	s.mu.Unlock()

	success := false
	defer func() {
		s.mu.Lock()
		s.rotating = false
		if !success {
			s.connected = false
		}
		s.mu.Unlock()
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
			lastErr = err
			continue
		}
		go s.watch(cmd)

		ip, err := waitTunnelUp(cmd)
		if err != nil {
			killProcess(cmd)
			lastErr = err
			slog.Warn("vpngate: connect attempt failed, trying another server", "server", server.HostName, "error", err)
			continue
		}

		s.mu.Lock()
		s.cmd = cmd
		s.cfgPath = cfgPath
		s.current = &server
		s.ip = ip
		s.connected = true
		s.connectedAt = time.Now()
		s.mu.Unlock()
		success = true
		return nil
	}
	return fmt.Errorf("rotation failed after %d attempt(s): %w", len(candidates), lastErr)
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
		if !matchCountry(s.country, sv) {
			continue
		}
		if s.minScore > 0 && sv.Score < s.minScore {
			continue
		}
		if s.maxPing > 0 {
			if p, ok := parsePing(sv.Ping); !ok || p > s.maxPing {
				continue
			}
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
// (routing/DNS not settled yet). It doubles as a liveness probe: after
// several consecutive egress failures it kills the tunnel so the reconnect
// loop replaces what is likely a dead-but-alive relay.
func (s *supervisor) ipRefresher() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	failures := 0
	for range ticker.C {
		s.mu.Lock()
		connected := s.connected && s.cmd != nil
		s.mu.Unlock()
		if !connected {
			failures = 0
			continue
		}
		if ip, err := fetchPublicIP(); err == nil && ip != "" {
			failures = 0
			s.mu.Lock()
			if s.connected && s.cmd != nil {
				s.ip = ip
			}
			s.mu.Unlock()
		} else {
			failures++
			// ~30s of dead egress → recycle the tunnel. Kill happens
			// outside the lock so the control API stays responsive
			// during the up-to-5s process teardown.
			if failures >= 2 {
				slog.Warn("vpngate: tunnel egress failing, forcing reconnect")
				s.mu.Lock()
				cmd, cfgPath := s.cmd, s.cfgPath
				s.cmd = nil
				s.cfgPath = ""
				s.connected = false
				s.mu.Unlock()
				if cmd != nil {
					killProcess(cmd)
				}
				if cfgPath != "" {
					os.Remove(cfgPath)
				}
				failures = 0
			}
		}
	}
}

// fetchPublicIP returns the public IP as seen through the tunnel, trying a
// second endpoint if the first fails. Free VPN relays are slow, so the
// client timeout gives the TLS round-trip room to breathe.
func fetchPublicIP() (string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	var lastErr error

	resp, err := client.Get("https://api.ipify.org?format=json")
	if err == nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var out struct {
				IP string `json:"ip"`
			}
			if err := json.Unmarshal(body, &out); err == nil && out.IP != "" {
				return out.IP, nil
			}
		}
		lastErr = fmt.Errorf("ipify: status %d", resp.StatusCode)
	} else {
		lastErr = err
	}

	resp, err = client.Get("https://ifconfig.me/ip")
	if err == nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			if ip := strings.TrimSpace(string(body)); ip != "" {
				return ip, nil
			}
		}
		lastErr = fmt.Errorf("ifconfig.me: status %d", resp.StatusCode)
	} else {
		lastErr = err
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

func matchCountry(filter string, s vpn.Server) bool {
	if filter == "" {
		return true
	}
	f := strings.ToLower(strings.TrimSpace(filter))
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
