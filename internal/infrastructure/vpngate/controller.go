// Package vpngate talks to the VPNGate/OpenVPN supervisor container
// (cmd/vpngate-supervisor). The supervisor maintains an OpenVPN tunnel with
// a SOCKS5 proxy and exposes a small HTTP control API: POST /rotate picks a
// random server, POST /connect connects to a chosen one, GET /servers lists
// candidates, GET /status and GET /ip report tunnel state. The dashboard
// uses these endpoints to let operators pick the exit server manually;
// there is no automatic rotation on upstream 429s.
package vpngate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// DefaultMinInterval is the minimum time between scheduled rotations.
	DefaultMinInterval = 30 * time.Second
	// requestTimeout bounds a single control-API call. Rotation blocks
	// until a usable tunnel is up, trying up to 3 servers (~70s worst
	// case), so allow headroom.
	requestTimeout = 100 * time.Second
)

// ServerInfo is one relay server offered by the supervisor's /servers
// endpoint (already filtered by the supervisor's configured filters).
type ServerInfo struct {
	Hostname    string `json:"hostname"`
	IP          string `json:"ip"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Score       int    `json:"score"`
	Ping        string `json:"ping"`
}

// StatusInfo is the supervisor's current tunnel state (GET /status).
type StatusInfo struct {
	Connected   bool   `json:"connected"`
	Server      string `json:"server"`
	Country     string `json:"country"`
	IP          string `json:"ip"`
	ConnectedAt int64  `json:"connected_at"`
}

// PingResult is the outcome of the supervisor's live connectivity check
// (POST /ping): DNS resolution, an HTTPS egress probe with latency, and
// the current tunnel state.
type PingResult struct {
	Connected bool   `json:"connected"`
	Server    string `json:"server"`
	Country   string `json:"country"`
	IP        string `json:"ip"`

	DNSOK     bool   `json:"dns_ok"`
	DNSMS     int64  `json:"dns_ms"`
	DNSError  string `json:"dns_error,omitempty"`

	EgressOK  bool   `json:"egress_ok"`
	EgressIP  string `json:"egress_ip,omitempty"`
	HTTPMS    int64  `json:"http_ms"`
	HTTPCode  int    `json:"http_code"`
	EgressErr string `json:"egress_error,omitempty"`
}

// Controller rotates the apparent exit IP by asking the supervisor to
// reconnect its OpenVPN tunnel to a different VPNGate server.
type Controller struct {
	ctrlURL     string
	minInterval time.Duration
	client      *http.Client

	mu      sync.Mutex
	lastRot time.Time

	currentIP string
	currentMu sync.RWMutex
}

// NewController builds a Controller talking to a supervisor at host:ctrlPort.
// minInterval throttles NewIP (ForceNewIP bypasses it).
func NewController(host string, ctrlPort int, minInterval time.Duration) *Controller {
	if minInterval <= 0 {
		minInterval = DefaultMinInterval
	}
	return &Controller{
		ctrlURL:     "http://" + net.JoinHostPort(host, strconv.Itoa(ctrlPort)),
		minInterval: minInterval,
		client:      &http.Client{Timeout: requestTimeout},
	}
}

// NewIP rotates the exit IP unless the minimum interval has not elapsed.
// Returns nil even when skipped.
func (c *Controller) NewIP() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	elapsed := time.Since(c.lastRot)
	if elapsed < c.minInterval {
		slog.Debug("vpngate: IP rotation skipped, too soon", "elapsed", elapsed.Round(time.Millisecond), "min", c.minInterval)
		return nil
	}
	return c.rotateLocked()
}

// ForceNewIP rotates immediately, ignoring the minimum interval.
// Used when the upstream returns 429.
func (c *Controller) ForceNewIP() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	slog.Info("vpngate: forcing IP rotation (bypassing interval)")
	return c.rotateLocked()
}

func (c *Controller) rotateLocked() error {
	resp, err := c.client.Post(c.ctrlURL+"/rotate", "application/json", nil)
	if err != nil {
		return fmt.Errorf("vpngate control rotate: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		IP string `json:"ip"`
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vpngate control rotate: unexpected status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("vpngate control rotate: decode: %w", err)
	}
	c.lastRot = time.Now()
	if out.IP != "" {
		c.setIP(out.IP)
	}
	slog.Info("vpngate: IP rotated", "ip", out.IP)
	return nil
}

// CurrentIP returns the last known tunnel IP (thread-safe, no network).
func (c *Controller) CurrentIP() string {
	c.currentMu.RLock()
	defer c.currentMu.RUnlock()
	return c.currentIP
}

func (c *Controller) setIP(ip string) {
	c.currentMu.Lock()
	c.currentIP = ip
	c.currentMu.Unlock()
}

// StartMonitor periodically refreshes the cached tunnel IP from the
// supervisor without triggering a rotation.
func (c *Controller) StartMonitor(interval time.Duration, stop <-chan struct{}) {
	c.refreshIP()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.refreshIP()
		case <-stop:
			slog.Info("vpngate: IP monitor stopped")
			return
		}
	}
}

// ListServers returns the relay servers currently offered by the
// supervisor (after its country/score/ping filters), so the dashboard can
// render a picker.
func (c *Controller) ListServers() ([]ServerInfo, error) {
	resp, err := c.client.Get(c.ctrlURL + "/servers")
	if err != nil {
		return nil, fmt.Errorf("vpngate control servers: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		Servers []ServerInfo `json:"servers"`
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vpngate control servers: unexpected status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("vpngate control servers: decode: %w", err)
	}
	return out.Servers, nil
}

// ConnectTo asks the supervisor to bring the tunnel up on the given relay
// server (hostname). It blocks until the tunnel is up or the connect
// fails, and surfaces the supervisor's error message on failure.
func (c *Controller) ConnectTo(hostname string) error {
	body, err := json.Marshal(map[string]string{"server": hostname})
	if err != nil {
		return fmt.Errorf("vpngate control connect: marshal: %w", err)
	}
	resp, err := c.client.Post(c.ctrlURL+"/connect", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("vpngate control connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg := readErrorBody(resp)
		return fmt.Errorf("vpngate control connect: status %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err == nil && out.IP != "" {
		c.setIP(out.IP)
	}
	slog.Info("vpngate: connected to server", "server", hostname)
	return nil
}

// Status returns the supervisor's current tunnel state.
func (c *Controller) Status() (StatusInfo, error) {
	resp, err := c.client.Get(c.ctrlURL + "/status")
	if err != nil {
		return StatusInfo{}, fmt.Errorf("vpngate control status: %w", err)
	}
	defer resp.Body.Close()

	var out StatusInfo
	if resp.StatusCode != http.StatusOK {
		return StatusInfo{}, fmt.Errorf("vpngate control status: unexpected status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return StatusInfo{}, fmt.Errorf("vpngate control status: decode: %w", err)
	}
	if out.IP != "" {
		c.setIP(out.IP)
	}
	return out, nil
}

// Ping asks the supervisor to run a live connectivity check through the
// tunnel and returns the result.
func (c *Controller) Ping() (PingResult, error) {
	resp, err := c.client.Post(c.ctrlURL+"/ping", "application/json", nil)
	if err != nil {
		return PingResult{}, fmt.Errorf("vpngate control ping: %w", err)
	}
	defer resp.Body.Close()

	var out PingResult
	if resp.StatusCode != http.StatusOK {
		return PingResult{}, fmt.Errorf("vpngate control ping: unexpected status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return PingResult{}, fmt.Errorf("vpngate control ping: decode: %w", err)
	}
	if out.IP != "" {
		c.setIP(out.IP)
	}
	return out, nil
}

// readErrorBody extracts the supervisor's {"error": "..."} message from a
// non-200 response, falling back to a truncated raw body.
func readErrorBody(resp *http.Response) string {
	var out struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out); err == nil && out.Error != "" {
		return out.Error
	}
	return "unknown error"
}

func (c *Controller) refreshIP() {
	resp, err := c.client.Get(c.ctrlURL + "/ip")
	if err != nil {
		slog.Debug("vpngate: failed to refresh IP", "error", err)
		return
	}
	defer resp.Body.Close()
	var out struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.IP == "" {
		slog.Debug("vpngate: bad IP response", "error", err)
		return
	}
	c.setIP(out.IP)
}

// Close logs shutdown; the supervisor container manages its own lifecycle.
func (c *Controller) Close() {
	slog.Info("vpngate: controller closed")
}
