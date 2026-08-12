// Package vpngate implements domain.IPRotator against the VPNGate/OpenVPN
// supervisor container (cmd/vpngate-supervisor). The supervisor maintains an
// OpenVPN tunnel with a SOCKS5 proxy and exposes a small HTTP control API:
// POST /rotate connects to a new server/IP, GET /ip reports the current
// tunnel IP.
package vpngate

import (
	"encoding/json"
	"fmt"
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
