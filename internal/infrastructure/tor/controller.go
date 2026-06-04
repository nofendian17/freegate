package tor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

const (
	// MinInterval is the minimum time between NEWNYM signals.
	// Tor requires at least ~10 seconds between signals for a new circuit.
	MinInterval = 10 * time.Second
)

type Controller struct {
	host       string
	port       int
	pass       string
	socks      string
	mu         sync.Mutex
	lastIP     time.Time
	currentIP  string
	currentMu  sync.RWMutex
}

func NewController(host string, port int, pass string, socksAddr string) *Controller {
	return &Controller{host: host, port: port, pass: pass, socks: socksAddr}
}

func (c *Controller) NewIP() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Enforce minimum interval between IP rotations
	elapsed := time.Since(c.lastIP)
	if elapsed < MinInterval {
		slog.Debug("tor: IP rotation skipped, too soon", "elapsed", elapsed.Round(time.Millisecond), "min", MinInterval)
		return nil // skip silently, IP won't change yet
	}

	return c.newIPLocked()
}

// ForceNewIP bypasses the minimum interval and forces a new Tor circuit immediately.
// Used when the upstream API returns 429 (rate limited) to get a fresh exit IP.
func (c *Controller) ForceNewIP() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	slog.Info("tor: forcing IP rotation (bypassing interval)")
	return c.newIPLocked()
}

func (c *Controller) newIPLocked() error {
	addr := net.JoinHostPort(c.host, strconv.Itoa(c.port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("tor control connect: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	cmd := fmt.Sprintf("AUTHENTICATE %q\r\nSIGNAL NEWNYM\r\nQUIT\r\n", c.pass)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return fmt.Errorf("tor control write: %w", err)
	}

	// Read response lines until we see a 250 reply code (OK) or an error.
	tp := textproto.NewReader(bufio.NewReader(conn))
	for range 20 {
		line, err := tp.ReadLine()
		if err != nil {
			return fmt.Errorf("tor control read: %w", err)
		}
		trimmed := strings.TrimSpace(line)
		if len(trimmed) >= 3 && trimmed[:3] == "250" {
			c.lastIP = time.Now()
			slog.Info("tor: IP rotated successfully")
			return nil
		}
		if len(trimmed) >= 3 && trimmed[0] == '5' {
			return fmt.Errorf("tor control unexpected: %s", trimmed)
		}
	}
	return fmt.Errorf("tor control: no OK response received")
}

// Close performs any cleanup needed for the Tor controller.
// Since each control connection is self-contained, this primarily logs shutdown.
func (c *Controller) Close() {
	slog.Info("tor: controller closed")
}

// getIP fetches the current exit IP by making an HTTP request through the SOCKS5 proxy.
// Updates the cached currentIP so the dashboard can read it without a fetch.
func (c *Controller) getIP() string {
	dialer, err := proxy.SOCKS5("tcp", c.socks, nil, proxy.Direct)
	if err != nil {
		slog.Debug("tor: failed to create SOCKS5 dialer for IP check", "error", err)
		return c.cacheIP("unknown")
	}

	tr := &http.Transport{
		DialContext: dialer.(proxy.ContextDialer).DialContext,
	}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	resp, err := client.Get("https://api.ipify.org?format=json")
	if err != nil {
		slog.Debug("tor: failed to fetch exit IP", "error", err)
		return c.cacheIP("unknown")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.cacheIP("unknown")
	}

	var result struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.IP == "" {
		return c.cacheIP("unknown")
	}
	return c.cacheIP(result.IP)
}

// CurrentIP returns the last known Tor exit IP (thread-safe, no network).
func (c *Controller) CurrentIP() string {
	c.currentMu.RLock()
	defer c.currentMu.RUnlock()
	return c.currentIP
}

func (c *Controller) cacheIP(ip string) string {
	c.currentMu.Lock()
	c.currentIP = ip
	c.currentMu.Unlock()
	return ip
}

// StartMonitor periodically fetches and logs the current Tor exit IP.
func (c *Controller) StartMonitor(interval time.Duration, stop <-chan struct{}) {
	// Log immediately on startup
	ip := c.getIP()
	slog.Info("tor: current exit IP", "ip", ip)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ip := c.getIP()
			slog.Info("tor: current exit IP", "ip", ip)
		case <-stop:
			slog.Info("tor: IP monitor stopped")
			return
		}
	}
}
