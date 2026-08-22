package vpn

import (
	"context"
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
	"sync"
	"time"

	"freegate/internal/infrastructure/vpngate"

	"github.com/davegallant/vpngate/pkg/vpn"
)

var execLookPath = exec.LookPath

type ProviderConfig struct {
	Enabled    bool
	Provider   string
	SocksAddr  string
	Country    string
	MinScore   int
	MaxPing    int
	RefreshInt time.Duration
}

type ServerInfo = vpngate.ServerInfo
type StatusInfo = vpngate.StatusInfo
type PingResult = vpngate.PingResult

type Provider interface {
	Start(ctx context.Context) error
	Rotate() error
	ConnectTo(hostname string) error
	ListServers() ([]ServerInfo, error)
	RefreshServers() ([]ServerInfo, error)
	Status() (StatusInfo, error)
	Ping() (PingResult, error)
	CurrentIP() string
	InstallHint() string
	Close() error
}

func NewProvider(cfg ProviderConfig) (Provider, error) {
	if !cfg.Enabled || cfg.Provider == "direct" {
		return &directProvider{}, nil
	}
	switch runtime.GOOS {
	case "linux":
		return newLinuxProvider(cfg), nil
	case "darwin":
		return newDarwinProvider(cfg), nil
	case "windows":
		return newWindowsProvider(cfg), nil
	default:
		return newLinuxProvider(cfg), nil
	}
}

type directProvider struct {
	mu sync.RWMutex
	ip string
}

func (d *directProvider) Start(ctx context.Context) error                     { return nil }
func (d *directProvider) Rotate() error                                       { return nil }
func (d *directProvider) ConnectTo(string) error                              { return nil }
func (d *directProvider) ListServers() ([]ServerInfo, error)                  { return nil, nil }
func (d *directProvider) RefreshServers() ([]ServerInfo, error)               { return nil, nil }
func (d *directProvider) Status() (StatusInfo, error)                         { return StatusInfo{}, nil }
func (d *directProvider) Ping() (PingResult, error)                           { return PingResult{Direct: true}, nil }
func (d *directProvider) CurrentIP() string                                   { return "direct" }
func (d *directProvider) InstallHint() string                                 { return "" }
func (d *directProvider) Close() error                                        { return nil }

func newLinuxProvider(cfg ProviderConfig) Provider   { return newSupervisorProvider(cfg, "linux") }
func newDarwinProvider(cfg ProviderConfig) Provider  { return newSupervisorProvider(cfg, "darwin") }
func newWindowsProvider(cfg ProviderConfig) Provider { return newSupervisorProvider(cfg, "windows") }

func newSupervisorProvider(cfg ProviderConfig, os string) Provider {
	sp := &supervisorProvider{cfg: cfg, os: os, socksAddr: cfg.SocksAddr, registry: newServerRegistry(cfg)}
	if sp.cfg.RefreshInt == 0 {
		sp.cfg.RefreshInt = 300 * time.Second
		// keep registry in sync
		sp.registry.cfg.RefreshInt = 300 * time.Second
	}
	if _, err := findOpenVPN(); err != nil {
		sp.direct = true
		sp.installHint = installHintForOS(runtime.GOOS)
	}
	return sp
}

var (
	errRotationInProgress = errors.New("rotation already in progress")
	errServerNotFound     = errors.New("server not found in list")
)

type supervisorProvider struct {
	cfg       ProviderConfig
	os        string
	socksAddr string
	mu          sync.Mutex
	rotating    bool
	cmd         *exec.Cmd
	cfgPath     string
	current     *vpn.Server
	ip          string
	connected   bool
	connectedAt time.Time
	registry    *serverRegistry
	installHint string
	direct      bool
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

func (s *supervisorProvider) Start(ctx context.Context) error {
	if _, err := findOpenVPN(); err != nil {
		slog.Warn("vpn: openvpn not found, falling back to direct mode", "os", s.os, "error", err, "hint", installHintForOS(runtime.GOOS))
		s.mu.Lock()
		s.direct = true
		s.installHint = installHintForOS(runtime.GOOS)
		s.mu.Unlock()
		return nil
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	// SOCKS
	go func() {
		if err := serveSOCKS(s.socksAddr); err != nil {
			slog.Error("vpn: socks server exited", "error", err)
		}
	}()
	// background loops
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

func (s *supervisorProvider) reconnectLoop() {
	for {
		select {
		case <-s.ctx.Done():
			slog.Info("vpn: reconnect loop stopped")
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
		slog.Info("vpn: connecting to a vpn server")
		if err := s.Rotate(); err != nil {
			if !errors.Is(err, errRotationInProgress) {
				slog.Warn("vpn: connect failed", "error", err)
			}
			time.Sleep(2 * time.Second)
			continue
		}
		slog.Info("vpn: connected", "server", s.serverName(), "ip", s.CurrentIP())
		time.Sleep(5 * time.Second)
	}
}

func (s *supervisorProvider) Rotate() error {
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
	s.mu.Lock()
	tried := map[string]bool{}
	if s.current != nil {
		tried[s.current.HostName] = true
	}
	s.mu.Unlock()
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
			slog.Warn("vpn: connect attempt failed, trying another server", "server", server.HostName, "error", err)
			continue
		}
		success = true
		return nil
	}
	return fmt.Errorf("rotation failed after %d attempt(s): %w", len(candidates), lastErr)
}

func (s *supervisorProvider) beginRotation() (func(), error) {
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

func (s *supervisorProvider) connectToServer(server vpn.Server) error {
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
		ip = server.IPAddr
	}
	s.ip = ip
	s.connected = true
	s.connectedAt = time.Now()
	s.mu.Unlock()
	return nil
}

func (s *supervisorProvider) ConnectTo(hostname string) error {
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

func (s *supervisorProvider) watch(cmd *exec.Cmd) {
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
		return
	}
	if err != nil {
		slog.Warn("vpn: openvpn exited", "error", err)
	} else {
		slog.Info("vpn: openvpn exited")
	}
}

func (s *supervisorProvider) serverMatchesFilters(sv vpn.Server) bool {
	return s.registry.matches(sv)
}

func (s *supervisorProvider) pickServer(tried map[string]bool) (vpn.Server, error) {
	return s.registry.pickServer(tried)
}

func (s *supervisorProvider) getServers() ([]vpn.Server, error) {
	return s.registry.getServers()
}

func (s *supervisorProvider) ListServers() ([]ServerInfo, error) {
	s.mu.Lock()
	isDirect := s.direct
	s.mu.Unlock()
	if isDirect {
		return nil, nil
	}
	return s.registry.listServers()
}

func (s *supervisorProvider) RefreshServers() ([]ServerInfo, error) {
	s.mu.Lock()
	isDirect := s.direct
	s.mu.Unlock()
	if isDirect {
		return nil, nil
	}
	return s.registry.refreshServers()
}

func (s *supervisorProvider) Status() (StatusInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return StatusInfo{Connected: s.connected, Server: s.serverNameLocked(), Country: s.countryLocked(), IP: s.ip, ConnectedAt: s.connectedAt.Unix()}, nil
}

func (s *supervisorProvider) Ping() (PingResult, error) {
	s.mu.Lock()
	isDirect := s.direct
	s.mu.Unlock()
	if isDirect {
		return PingResult{Direct: true}, nil
	}
	// simplified ping via supervisor's ping logic: DNS + egress
	return s.ping(), nil
}

func (s *supervisorProvider) ping() PingResult {
	res := PingResult{}
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	dnsStart := time.Now()
	_, err := net.DefaultResolver.LookupHost(ctx, "opencode.ai")
	dnsMS := time.Since(dnsStart).Milliseconds()
	cancel()
	res.DNSMS = dnsMS
	if err != nil {
		res.DNSError = err.Error()
	} else {
		res.DNSOK = true
	}
	client := &http.Client{Timeout: 12 * time.Second}
	for _, u := range []string{"https://api.ipify.org", "https://ifconfig.me/ip"} {
		start := time.Now()
		resp, err := client.Get(u)
		ms := time.Since(start).Milliseconds()
		res.HTTPMS = ms
		if err != nil {
			res.EgressErr = err.Error()
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
		} else {
			resp.Body.Close()
		}
	}
	if res.EgressErr == "" {
		res.EgressErr = "no egress endpoint reachable"
	}
	return res
}

func (s *supervisorProvider) CurrentIP() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.direct {
		return "direct"
	}
	return s.ip
}

func (s *supervisorProvider) InstallHint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installHint
}

func (s *supervisorProvider) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
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
	return nil
}

func (s *supervisorProvider) isConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected && s.cmd != nil
}
func (s *supervisorProvider) isRotating() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotating
}
func (s *supervisorProvider) serverName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serverNameLocked()
}
func (s *supervisorProvider) serverNameLocked() string {
	if s.current == nil {
		return ""
	}
	return s.current.HostName
}
func (s *supervisorProvider) countryLocked() string {
	if s.current == nil {
		return ""
	}
	return s.current.CountryLong
}

func (s *supervisorProvider) ipRefresher() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		connected := s.connected && s.cmd != nil
		s.mu.Unlock()
		if !connected {
			continue
		}
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
			if attempt+1 < ipRefreshAttempts {
				time.Sleep(ipRefreshRetryDelay)
			}
		}
	}
}

