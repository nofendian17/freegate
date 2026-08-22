package vpn

import (
	"context"
	"log/slog"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"freegate/internal/infrastructure/vpngate"
)

var execLookPath = exec.LookPath

type ProviderConfig struct {
	Enabled    bool
	Provider   string // auto|vpngate|direct
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
	sp := &supervisorProvider{cfg: cfg, os: os, socksAddr: cfg.SocksAddr}
	// Pre-check at construction so dialer can fallback immediately even before Start().
	if _, err := findOpenVPN(); err != nil {
		sp.direct = true
		sp.installHint = installHintForOS(runtime.GOOS)
	}
	return sp
}

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
		if p, err := execLookPath(bin); err == nil {
			return p, nil
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

type supervisorProvider struct {
	cfg         ProviderConfig
	os          string
	socksAddr   string
	mu          sync.RWMutex
	direct      bool
	installHint string
	currentIP   string
	connected   bool
	server      string
	country     string
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
	// Best-effort SOCKS; failure is not fatal (direct fallback).
	go func() {
		if err := serveSOCKS(s.socksAddr); err != nil {
			slog.Error("vpn: socks server exited", "error", err)
		}
	}()
	s.mu.Lock()
	s.connected = false
	s.mu.Unlock()
	return nil
}

func (s *supervisorProvider) Rotate() error {
	s.mu.RLock()
	isDirect := s.direct
	s.mu.RUnlock()
	if isDirect {
		return nil
	}
	return nil
}

func (s *supervisorProvider) ConnectTo(hostname string) error {
	s.mu.RLock()
	isDirect := s.direct
	s.mu.RUnlock()
	if isDirect {
		return nil
	}
	return nil
}

func (s *supervisorProvider) ListServers() ([]ServerInfo, error) {
	s.mu.RLock()
	isDirect := s.direct
	s.mu.RUnlock()
	if isDirect {
		return nil, nil
	}
	list, err := fetchServerList(false)
	if err != nil {
		return nil, err
	}
	out := make([]ServerInfo, 0, len(*list))
	for _, sv := range *list {
		if !matchCountry(s.cfg.Country, sv) {
			continue
		}
		if s.cfg.MinScore > 0 && sv.Score < s.cfg.MinScore {
			continue
		}
		if s.cfg.MaxPing > 0 {
			if p, ok := parsePing(sv.Ping); !ok || p > s.cfg.MaxPing {
				continue
			}
		}
		out = append(out, ServerInfo{
			Hostname:    sv.HostName,
			IP:          sv.IPAddr,
			Country:     sv.CountryLong,
			CountryCode: sv.CountryShort,
			Score:       sv.Score,
			Ping:        sv.Ping,
		})
	}
	return out, nil
}

func (s *supervisorProvider) RefreshServers() ([]ServerInfo, error) {
	s.mu.RLock()
	isDirect := s.direct
	s.mu.RUnlock()
	if isDirect {
		return nil, nil
	}
	list, err := fetchServerList(true)
	if err != nil {
		return nil, err
	}
	out := make([]ServerInfo, 0, len(*list))
	for _, sv := range *list {
		out = append(out, ServerInfo{
			Hostname: sv.HostName, IP: sv.IPAddr, Country: sv.CountryLong, CountryCode: sv.CountryShort, Score: sv.Score, Ping: sv.Ping,
		})
	}
	return out, nil
}

func (s *supervisorProvider) Status() (StatusInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return StatusInfo{
		Connected:   s.connected && !s.direct,
		Server:      s.server,
		Country:     s.country,
		IP:          s.currentIP,
		ConnectedAt: 0,
	}, nil
}

func (s *supervisorProvider) Ping() (PingResult, error) {
	s.mu.RLock()
	isDirect := s.direct
	s.mu.RUnlock()
	if isDirect {
		return PingResult{Direct: true}, nil
	}
	s.mu.RLock()
	ip := s.currentIP
	srv := s.server
	ctry := s.country
	connected := s.connected
	s.mu.RUnlock()
	return PingResult{Connected: connected, Direct: false, Server: srv, Country: ctry, IP: ip}, nil
}

func (s *supervisorProvider) CurrentIP() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.direct {
		return "direct"
	}
	return s.currentIP
}

func (s *supervisorProvider) InstallHint() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.installHint
}

func (s *supervisorProvider) Close() error { return nil }
