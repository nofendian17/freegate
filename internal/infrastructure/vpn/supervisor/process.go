package supervisor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/davegallant/vpngate/pkg/vpn"
)

// lookPath is overridable in tests.
var lookPath = exec.LookPath

func openVPNCandidatesForOS(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"openvpn", "/opt/homebrew/bin/openvpn", "/usr/local/bin/openvpn"}
	case "windows":
		return []string{"openvpn.exe", "openvpn"}
	default:
		return []string{"openvpn"}
	}
}

func findOpenVPN() (string, error) {
	for _, bin := range openVPNCandidatesForOS(runtime.GOOS) {
		if p, err := lookPath(bin); err == nil {
			return p, err
		}
	}
	return "", exec.ErrNotFound
}

func installHintForOS(goos string) string {
	switch goos {
	case "darwin":
		return "brew install openvpn"
	case "windows":
		return "winget install OpenVPNTechnologies.OpenVPN  (or choco install openvpn)"
	default:
		return "sudo apt install openvpn  (or sudo yum install openvpn / sudo pacman -S openvpn)"
	}
}

// managedProcess pairs an openvpn child process with its temp config path
// and a done channel closed by the watcher goroutine once cmd.Wait
// reaps the exit. All teardown paths synchronize on done, so stop() is
// free of the ProcessState data race and works portably (no syscall.Kill).
type managedProcess struct {
	cmd     *exec.Cmd
	cfgPath string
	done    chan struct{}
}

// startOpenVPN decodes the server's base64 OpenVPN config and launches
// the openvpn binary with it.
func startOpenVPN(bin string, server vpn.Server) (*managedProcess, error) {
	decoded, err := base64.StdEncoding.DecodeString(server.OpenVpnConfigData)
	if err != nil {
		return nil, fmt.Errorf("decode openvpn config: %w", err)
	}
	tmp, err := os.CreateTemp("", "fg-vpngate-*.ovpn")
	if err != nil {
		return nil, fmt.Errorf("create config file: %w", err)
	}
	if _, err := tmp.Write(decoded); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("write config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}

	cmd := exec.Command(bin,
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
		return nil, fmt.Errorf("start openvpn: %w", err)
	}
	return &managedProcess{
		cmd:     cmd,
		cfgPath: tmp.Name(),
		done:    make(chan struct{}),
	}, nil
}

// stop terminates an openvpn process gracefully (SIGTERM where the OS
// supports signals), then forcefully after the grace period. It blocks
// until the watcher has reaped the process.
func (m *managedProcess) stop(grace time.Duration) {
	if m == nil || m.cmd == nil || m.cmd.Process == nil {
		return
	}
	_ = m.cmd.Process.Signal(os.Interrupt)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-m.done:
		return
	case <-timer.C:
	}
	pid := m.cmd.Process.Pid
	slog.Warn("vpngate: openvpn did not exit in time, killing", "pid", pid)
	_ = m.cmd.Process.Kill()
	<-m.done
}

// removeCfg deletes the temp .ovpn config once, idempotently.
func (m *managedProcess) removeCfg() {
	if m == nil || m.cfgPath == "" {
		return
	}
	os.Remove(m.cfgPath)
	m.cfgPath = ""
}

// snapshotIfaces records the interface names present right now, so a
// tunnel device created afterwards can be recognized as new.
func snapshotIfaces() map[string]bool {
	before := map[string]bool{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return before // empty snapshot: every iface looks "new"; gate stays permissive
	}
	for _, ifc := range ifaces {
		before[ifc.Name] = true
	}
	return before
}

// tunnelWaitTimeout bounds how long a single openvpn attempt may take to
// bring the tunnel interface up.
const tunnelWaitTimeout = 8 * time.Second

// ipCheckTimeout bounds the best-effort egress check after the tunnel
// interface is up. A slow-but-usable relay is not rejected here; the
// ipRefresher reaps tunnels that stop routing.
const ipCheckTimeout = 6 * time.Second

// waitTunnelUp blocks until a NEW tunnel interface appears and is up
// (platforms where detection is meaningful), then does a short best-effort
// egress check. It fails fast if the openvpn process exits (e.g.
// AUTH_FAILED or a dead relay) instead of waiting out the full timeout.
// A slow-but-usable tunnel is still accepted; the ipRefresher reaps
// tunnels that stop routing entirely.
//
// On platforms without predictable adapter names (Windows) the interface
// gate is skipped: process liveness plus the egress probe decide.
func waitTunnelUp(m *managedProcess, before map[string]bool) (string, error) {
	if tunGateEnabled() {
		deadline := time.Now().Add(tunnelWaitTimeout)
		for !newTunnelUp(before) {
			select {
			case <-m.done:
				return "", fmt.Errorf("openvpn exited before the tunnel interface came up")
			default:
			}
			if time.Now().After(deadline) {
				return "", fmt.Errorf("openvpn tunnel interface did not come up within %s", tunnelWaitTimeout)
			}
			time.Sleep(500 * time.Millisecond)
		}
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

// newTunnelUp reports whether an interface created after the snapshot is
// a TUN/TAP device in up state.
func newTunnelUp(before map[string]bool) bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifc := range ifaces {
		if before[ifc.Name] || !isTunName(ifc.Name) {
			continue
		}
		if ifc.Flags&net.FlagUp != 0 {
			return true
		}
	}
	return false
}

// ipEchoProbe describes a public service that answers with the caller's
// public IP. jsonKey selects a field in a JSON response; empty means the
// body is the bare IP.
type ipEchoProbe struct {
	url     string
	jsonKey string
}

// ipEchoProbes are tried in order by fetchPublicIP. Package-level so
// tests can point them at local servers.
var ipEchoProbes = []ipEchoProbe{
	{url: "https://api.ipify.org?format=json", jsonKey: "ip"},
	{url: "https://ifconfig.me/ip"},
}

// probeClient shares connections across every IP/egress probe so repeated
// checks reuse pooled connections instead of paying a fresh TLS handshake
// through slow relays each time. Timeouts are applied per request.
var probeClient = &http.Client{}

// probeIP fetches one echo endpoint and returns the public IP it reports.
// The timeout is per endpoint so one slow or blackholed service cannot eat
// the whole attempt.
func probeIP(p ipEchoProbe) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return "", err
	}
	resp, err := probeClient.Do(req)
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
