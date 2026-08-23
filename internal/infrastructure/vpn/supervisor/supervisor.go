package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/davegallant/vpngate/pkg/vpn"
)

var (
	// ErrRotationInProgress is returned by Rotate when another rotation
	// (e.g. from the reconnect loop) is already running.
	ErrRotationInProgress = errors.New("rotation already in progress")
	// ErrServerNotFound is returned by ConnectTo when the requested
	// hostname is not in the (filtered) server list.
	ErrServerNotFound = errors.New("server not found in list")
)

// ipRefreshAttempts / ipRefreshRetryDelay give the background IP refresher
// patience with slow free relays instead of giving up after one timed-out
// probe per tick.
const (
	ipRefreshAttempts   = 3
	ipRefreshRetryDelay = 3 * time.Second
)

// rotateAttempts is how many different servers a single rotation tries
// before giving up (free VPNGate relays are often dead or full).
const rotateAttempts = 3

// pingProbeTargets are the hosts/URLs the live connectivity check probes
// through the tunnel. They are package-level vars so tests can point them
// at local servers instead of the live internet.
var (
	pingDNSHost   = "opencode.ai"
	pingEgressURL = []string{"https://api.ipify.org", "https://ifconfig.me/ip"}
)

// Supervisor owns the tunnel lifecycle and the current connection state.
type Supervisor struct {
	cfg      Config
	registry *serverRegistry

	socksAddr string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu          sync.Mutex
	rotating    bool
	cur         *managedProcess
	current     *vpn.Server
	ip          string
	connected   bool
	connectedAt time.Time

	direct      bool
	installHint string

	// refreshBusy keeps the IP refresher single-flight: a probe cycle can
	// outlast its tick (3 attempts × slow timeouts), and piling cycles up
	// would hammer echo services through an already-slow relay.
	refreshBusy atomic.Bool
}

// New builds a Supervisor. When the openvpn binary cannot be found the
// supervisor degrades to direct mode (no tunnel attempts) and InstallHint
// reports how to install it; Start still succeeds so callers can run.
func New(cfg Config) *Supervisor {
	if cfg.RefreshInt == 0 {
		cfg.RefreshInt = 300 * time.Second
	}
	s := &Supervisor{
		cfg:       cfg,
		registry:  newServerRegistry(cfg),
		socksAddr: cfg.SocksAddr,
	}
	if _, err := findOpenVPN(); err != nil {
		s.direct = true
		s.installHint = installHintForOS(runtime.GOOS)
	}
	return s
}

// IsDirect reports whether openvpn was not found and the supervisor is
// running without a tunnel.
func (s *Supervisor) IsDirect() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.direct
}

// Start launches the SOCKS proxy and the background loops (reconnect +
// IP refresher). In direct mode it only logs and succeeds.
func (s *Supervisor) Start(ctx context.Context) error {
	if s.IsDirect() {
		slog.Warn("vpngate: openvpn not found, falling back to direct mode",
			"hint", s.InstallHint())
		return nil
	}
	s.ctx, s.cancel = context.WithCancel(ctx)

	go func() {
		if err := serveSOCKS(s.socksAddr); err != nil {
			slog.Error("vpngate: socks server exited", "error", err)
		}
	}()

	// Background loops — tracked by wg so shutdown waits for them.
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.reconnectLoop()
	}()
	go func() {
		defer s.wg.Done()
		s.ipRefresher()
	}()
	return nil
}

// reconnectLoop keeps trying until a tunnel is up, and reconnects
// whenever the openvpn process exits unexpectedly. While disconnected it
// retries aggressively (2s) to minimize dead-tunnel windows. When another
// rotation is already running (e.g. a request-triggered rotate), it waits
// silently instead of logging or racing it.
func (s *Supervisor) reconnectLoop() {
	for {
		select {
		case <-s.ctx.Done():
			slog.Info("vpngate: reconnect loop stopped")
			return
		default:
		}
		if s.isConnected() {
			time.Sleep(5 * time.Second)
			continue
		}
		if s.isRotating() {
			time.Sleep(2 * time.Second)
			continue
		}
		slog.Info("vpngate: connecting to a vpn server")
		if err := s.Rotate(); err != nil {
			if !errors.Is(err, ErrRotationInProgress) {
				slog.Warn("vpngate: connect failed", "error", err)
			}
			time.Sleep(2 * time.Second)
			continue
		}
		slog.Info("vpngate: connected", "server", s.serverName(), "ip", s.CurrentIP())
		time.Sleep(5 * time.Second)
	}
}

// Rotate tears down the current tunnel (if any) and connects to a
// different server. Free VPNGate relays are flaky, so it tries up to
// rotateAttempts servers before giving up. It blocks until a tunnel is up
// or all attempts fail.
//
// Only one rotation runs at a time: concurrent callers (e.g. the reconnect
// loop racing an explicit rotate request) are no-ops. The mutex is only
// held for short state reads/writes, never while a tunnel is being brought
// up, so control endpoints stay responsive during a slow rotation.
func (s *Supervisor) Rotate() error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
	}

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
		server, err := s.registry.pickServer(tried)
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
		// Stop trying further candidates once shutdown started: a
		// tunnel spawned now would outlive Close.
		if err := s.shutdownStarted(); err != nil {
			return err
		}
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

// shutdownStarted reports context cancellation so long-running work can
// bail out instead of racing Close. Nil-safe for supervisors constructed
// without Start (tests).
func (s *Supervisor) shutdownStarted() error {
	if s.ctx == nil {
		return nil
	}
	select {
	case <-s.ctx.Done():
		return fmt.Errorf("supervisor shutting down: %w", s.ctx.Err())
	default:
		return nil
	}
}

// beginRotation acquires the single-rotation guard, or returns
// ErrRotationInProgress if another rotation is already running. The
// returned func releases the guard; call it via defer.
func (s *Supervisor) beginRotation() (func(), error) {
	s.mu.Lock()
	if s.rotating {
		s.mu.Unlock()
		return nil, ErrRotationInProgress
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
// tunnel is being killed or brought up, so control endpoints stay
// responsive during a slow connect.
func (s *Supervisor) connectToServer(server vpn.Server) error {
	// Don't spawn a new tunnel while shutting down: a process started
	// after Close ran would be orphaned holding the tun device.
	if err := s.shutdownStarted(); err != nil {
		return err
	}
	// Tear down the previous tunnel outside the lock so control
	// endpoints stay responsive while the process is being killed.
	s.mu.Lock()
	old := s.cur
	s.cur = nil
	s.connected = false
	s.mu.Unlock()
	if old != nil {
		old.stop(5 * time.Second)
		old.removeCfg()
	}

	// stop() blocks until the process is reaped, which also destroys its
	// tun device — no extra wait-for-device-down is needed before the
	// next connect. Snapshot the interface list first so the new tunnel
	// device can be told apart from pre-existing ones (system utuns on
	// macOS, for example).
	before := snapshotIfaces()

	bin, err := findOpenVPN()
	if err != nil {
		return fmt.Errorf("openvpn binary: %w", err)
	}
	m, err := startOpenVPN(bin, server)
	if err != nil {
		return err
	}
	go s.watch(m)

	ip, err := waitTunnelUp(m, before)
	if err != nil {
		m.stop(5 * time.Second)
		return err
	}

	s.mu.Lock()
	s.cur = m
	s.current = &server
	if ip == "" {
		// The measured egress IP is often blank on slow relays: the
		// check right after the tun device is up is best-effort and
		// frequently times out. The relay's own address is a reliable
		// stand-in — traffic exits at the relay — so /ip and the
		// dashboard label stay populated instead of showing "—" until
		// the refresher refines the value.
		ip = server.IPAddr
	}
	s.ip = ip
	s.connected = true
	s.connectedAt = time.Now()
	s.mu.Unlock()
	return nil
}

// ConnectTo resolves a hostname from the cached server list and connects
// to it, holding the rotation guard. This is the explicit user-driven
// path. Only servers that pass the configured filters are connectable, so
// the picker and the direct API agree on what the operator may select.
func (s *Supervisor) ConnectTo(hostname string) error {
	servers, err := s.registry.getServers()
	if err != nil {
		return fmt.Errorf("fetch server list: %w", err)
	}
	for _, sv := range servers {
		if sv.HostName == hostname && s.registry.matches(sv) {
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
	return ErrServerNotFound
}

// watch waits for an openvpn process and marks the tunnel disconnected if
// it was still the active one. It closes the process's done channel once
// Wait has reaped the exit.
func (s *Supervisor) watch(m *managedProcess) {
	err := m.cmd.Wait()
	close(m.done)

	s.mu.Lock()
	isCurrent := s.cur == m
	if isCurrent {
		s.cur = nil
		s.connected = false
	}
	s.mu.Unlock()
	m.removeCfg()

	if !isCurrent {
		return // a newer process replaced this one
	}
	if err != nil {
		slog.Warn("vpngate: openvpn exited", "error", err)
	} else {
		slog.Info("vpngate: openvpn exited")
	}
}

// ListServers returns the relay servers currently offered (after filters)
// so a dashboard can render a picker.
func (s *Supervisor) ListServers() ([]ServerInfo, error) {
	if s.IsDirect() {
		return nil, nil
	}
	return s.registry.listServers()
}

// RefreshServers forces a re-fetch of the live vpngate list (ignoring the
// refresh interval) and returns the freshly filtered relays.
func (s *Supervisor) RefreshServers() ([]ServerInfo, error) {
	if s.IsDirect() {
		return nil, nil
	}
	return s.registry.refreshServers()
}

// Status returns the supervisor's current tunnel state.
func (s *Supervisor) Status() StatusInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return StatusInfo{
		Connected:   s.connected,
		Server:      s.serverNameLocked(),
		Country:     s.countryLocked(),
		IP:          s.ip,
		ConnectedAt: s.connectedAt.Unix(),
	}
}

// Healthy reports whether the service should be considered up by health
// checks: connected, or mid-rotation (it either succeeds or the reconnect
// loop keeps retrying until it does).
func (s *Supervisor) Healthy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotating || (s.connected && s.cur != nil)
}

// Ping performs a live connectivity check through the tunnel: DNS
// resolution, then an HTTPS GET to a public IP echo service measuring
// round-trip latency. This answers "is the VPN actually routing traffic?"
// beyond the device being up — a tunnel can be connected while the relay
// or the route is dead.
func (s *Supervisor) Ping() PingResult {
	res := PingResult{}
	s.mu.Lock()
	res.Connected = s.connected && s.cur != nil
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
	// broken. Shares probeClient's connection pool; timeout per request.
	var lastErr string
	for _, u := range pingEgressURL {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		httpStart := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			cancel()
			lastErr = err.Error()
			continue
		}
		resp, err := probeClient.Do(req)
		httpMS := time.Since(httpStart).Milliseconds()
		cancel()
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

// CurrentIP returns the last known tunnel IP, or the literal "direct"
// when no openvpn binary was found at construction.
func (s *Supervisor) CurrentIP() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.direct {
		return "direct"
	}
	return s.ip
}

// InstallHint returns an OS-specific hint for installing openvpn when the
// supervisor fell back to direct mode.
func (s *Supervisor) InstallHint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installHint
}

// Close stops background loops, kills the active tunnel, and removes its
// temp config.
func (s *Supervisor) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()

	s.mu.Lock()
	cur := s.cur
	s.cur = nil
	s.mu.Unlock()
	if cur != nil {
		cur.stop(5 * time.Second)
		cur.removeCfg()
	}
	return nil
}

func (s *Supervisor) isConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected && s.cur != nil
}

func (s *Supervisor) isRotating() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotating
}

func (s *Supervisor) serverName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serverNameLocked()
}

func (s *Supervisor) serverNameLocked() string {
	if s.current == nil {
		return ""
	}
	return s.current.HostName
}

func (s *Supervisor) countryLocked() string {
	if s.current == nil {
		return ""
	}
	return s.current.CountryLong
}

// ipRefresher periodically re-checks the tunnel's public IP so /ip and the
// dashboard stay accurate even when the check right after connecting fails
// (routing/DNS not settled yet). It deliberately does NOT kill the tunnel:
// a slow egress check used to recycle healthy relays every ~40s. Dead
// tunnels are detected by openvpn itself (ping-restart / connect-retry-max)
// and replaced by the reconnect loop when the process exits.
func (s *Supervisor) ipRefresher() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			slog.Info("vpngate: IP refresher stopped")
			return
		case <-ticker.C:
		}
		// Skip this tick if the previous probe cycle is still running
		// (single-flight): cycles can outlast the 15s tick on slow relays.
		if !s.refreshBusy.CompareAndSwap(false, true) {
			continue
		}
		s.refreshCycle()
		s.refreshBusy.Store(false)
	}
}

// refreshCycle runs one connected-check + bounded-retry IP probe.
func (s *Supervisor) refreshCycle() {
	s.mu.Lock()
	connected := s.connected && s.cur != nil
	s.mu.Unlock()
	if !connected {
		return
	}
	// Egress probes through free relays routinely need a few tries
	// before any echo service answers. Retry inside the tick so a
	// slow-but-working tunnel fills the label in this cycle instead
	// of waiting for the next tick.
	for attempt := 0; attempt < ipRefreshAttempts; attempt++ {
		ip, err := fetchPublicIP()
		if err == nil && ip != "" {
			s.mu.Lock()
			if s.connected && s.cur != nil {
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
