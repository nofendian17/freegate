package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/davegallant/vpngate/pkg/vpn"
)

func (s *Supervisor) reconnectLoop(ctx context.Context) {
	for {
		if !sleepCtx(ctx, 0) {
			slog.Info("vpngate: reconnect loop stopped")
			return
		}
		if s.isConnected() {
			if !sleepCtx(ctx, 5*time.Second) {
				slog.Info("vpngate: reconnect loop stopped")
				return
			}
			continue
		}
		if s.isRotating() {
			if !sleepCtx(ctx, 2*time.Second) {
				slog.Info("vpngate: reconnect loop stopped")
				return
			}
			continue
		}
		slog.Info("vpngate: connecting to a vpn server")
		if err := s.Rotate(); err != nil {
			// Shutdown-triggered aborts are expected, not failures.
			if !errors.Is(err, ErrRotationInProgress) && !errors.Is(err, context.Canceled) {
				slog.Warn("vpngate: connect failed", "error", err)
			}
			if !sleepCtx(ctx, 2*time.Second) {
				slog.Info("vpngate: reconnect loop stopped")
				return
			}
			continue
		}
		slog.Info("vpngate: connected", "server", s.serverName(), "ip", s.CurrentIP())
		if !sleepCtx(ctx, 5*time.Second) {
			slog.Info("vpngate: reconnect loop stopped")
			return
		}
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
	// Registering before the rotation guard keeps Close's accounting
	// conservative: it may wait for a rotation that then no-ops on the
	// guard, never the reverse.
	if err := s.enterConnect(); err != nil {
		return err
	}
	defer s.connects.Done()

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
// without Start (tests). Reads ctx under lock to stay race-free with Start.
func (s *Supervisor) shutdownStarted() error {
	s.mu.Lock()
	ctx := s.ctx
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return context.Canceled
	}
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("supervisor shutting down: %w", ctx.Err())
	default:
		return nil
	}
}

// enterConnect registers an in-flight connect attempt. It fails once
// shutdown has started, so a caller that passes here is guaranteed that
// Close's connects.Wait (which runs after cancel) will wait for it — closing
// the window where a tunnel could be spawned after Close returns.
// The closed check and connects.Add are atomic under mu, so Add can never
// run concurrently with Close's Wait when the counter is zero.
// Pair every successful call with s.connects.Done() via defer.
func (s *Supervisor) enterConnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return context.Canceled
	}
	if s.ctx != nil {
		select {
		case <-s.ctx.Done():
			return fmt.Errorf("supervisor shutting down: %w", s.ctx.Err())
		default:
		}
	}
	s.connects.Add(1)
	return nil
}

// sleepCtx waits for d, returning false as soon as ctx is cancelled so
// background loops exit promptly on shutdown instead of sleeping through
// it. Nil ctx sleeps the full duration.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
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
	// Bounds this connect attempt; Background when never started (tests).
	ctx := s.getCtx()
	if ctx == nil {
		ctx = context.Background()
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
	// Deliberately NOT tracked by the supervisor's WaitGroup: watch blocks
	// on cmd.Wait until the process exits, and Close kills the process
	// only after wg.Wait — adding it would deadlock shutdown. The done
	// channel below is the synchronization point instead.
	go s.watch(m)

	ip, err := waitTunnelUp(ctx, m, before)
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
	// Flush point for pooled upstream connections: fires on every
	// successful connect regardless of path (reconnect loop, manual
	// Rotate, ConnectTo) so stale conns never survive a tunnel change.
	if s.cfg.OnConnect != nil {
		s.cfg.OnConnect()
	}
	return nil
}

// ConnectTo resolves a hostname from the cached server list and connects
// to it, holding the rotation guard. This is the explicit user-driven
// path. Only servers that pass the configured filters are connectable, so
// the picker and the direct API agree on what the operator may select.
func (s *Supervisor) ConnectTo(hostname string) error {
	if err := s.enterConnect(); err != nil {
		return err
	}
	defer s.connects.Done()

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
