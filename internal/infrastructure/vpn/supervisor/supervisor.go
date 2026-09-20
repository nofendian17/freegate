package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime"
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
// All ctx/cancel/closed accesses are guarded by mu; use getCtx() instead
// of reading s.ctx directly so Start racing Rotate/Close is race-free.
// connects.Add is paired with the closed check under the same lock so
// Close's connects.Wait can never run concurrently with Add when the
// counter is zero (sync: WaitGroup misuse).
type Supervisor struct {
	cfg      Config
	registry *serverRegistry

	socksAddr string

	ctx    context.Context
	cancel context.CancelFunc
	// closed is set by Close under mu; enterConnect fails once set so no
	// new Add can race with connects.Wait.
	closed bool
	wg     sync.WaitGroup

	// connects tracks in-flight connect attempts from API callers so a
	// Close racing a Rotate/ConnectTo waits for the attempt to observe
	// cancellation instead of returning while a spawn is still possible.
	connects sync.WaitGroup

	mu          sync.Mutex
	rotating    bool
	cur         *managedProcess
	current     *vpn.Server
	ip          string
	connected   bool
	connectedAt time.Time

	direct      bool
	installHint string

	// socksLn is the SOCKS listener created by Start; Close shuts it down
	// so the in-process provider does not leak the accept goroutine.
	socksLn net.Listener

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

// getCtx returns the run context under lock. Nil when never started
// (direct mode / tests).
func (s *Supervisor) getCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctx
}

// Start launches the SOCKS proxy and the background loops (reconnect +
// IP refresher). In direct mode it only logs and succeeds. The SOCKS
// listener is created synchronously so bind errors surface to the caller.
func (s *Supervisor) Start(ctx context.Context) error {
	if s.IsDirect() {
		slog.Warn("vpngate: openvpn not found, falling back to direct mode",
			"hint", s.InstallHint())
		return nil
	}

	ln, err := net.Listen("tcp", s.socksAddr)
	if err != nil {
		return fmt.Errorf("socks listen %s: %w", s.socksAddr, err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.socksLn = ln
	s.ctx = runCtx
	s.cancel = cancel
	s.closed = false
	s.mu.Unlock()

	// Background goroutines — tracked via WaitGroup.Go so shutdown waits
	// for them. Capture runCtx once so loops never re-read s.ctx (race).
	s.wg.Go(func() {
		if err := serveSOCKS(ln); err != nil {
			if errors.Is(err, net.ErrClosed) {
				slog.Info("vpngate: socks server stopped")
			} else {
				slog.Error("vpngate: socks server exited", "error", err)
			}
		}
	})
	s.wg.Go(func() { s.reconnectLoop(runCtx) })
	s.wg.Go(func() { s.ipRefresher(runCtx) })
	return nil
}

// reconnectLoop keeps trying until a tunnel is up, and reconnects
// whenever the openvpn process exits unexpectedly. While disconnected it
// retries aggressively (2s) to minimize dead-tunnel windows. When another
// rotation is already running (e.g. a request-triggered rotate), it waits
// silently instead of logging or racing it.

// Close stops background loops, shuts down the SOCKS listener, kills the
// active tunnel, and removes its temp config.
func (s *Supervisor) Close() error {
	// Mark closed and grab listener + cancel under one lock so no new
	// enterConnect.Add can slip in after we start waiting.
	s.mu.Lock()
	ln := s.socksLn
	s.socksLn = nil
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}

	if cancel != nil {
		cancel()
	}
	// Wait for background loops AND any in-flight API connect attempt:
	// a Rotate/ConnectTo that passed enterConnect before cancel may still
	// be between checks; it observes ctx cancellation at its next
	// shutdownStarted/connectToServer gate and aborts without spawning.
	s.wg.Wait()
	s.connects.Wait()

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
