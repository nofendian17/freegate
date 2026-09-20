package supervisor

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

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
	// Direct mode has no tunnel to probe; report it as such instead of
	// failing DNS/egress checks (pre-consolidation parity).
	if s.direct {
		s.mu.Unlock()
		return PingResult{Direct: true}
	}
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
			lastErr = fmt.Sprintf("status %d, empty body", resp.StatusCode)
		} else {
			resp.Body.Close()
			lastErr = fmt.Sprintf("status %d", resp.StatusCode)
		}
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
