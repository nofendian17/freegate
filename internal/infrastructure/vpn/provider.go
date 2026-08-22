package vpn

import (
	"context"
	"runtime"
	"sync"
	"time"
)

type ProviderConfig struct {
	Enabled    bool
	Provider   string // auto|vpngate|direct
	SocksAddr  string
	Country    string
	MinScore   int
	MaxPing    int
	RefreshInt time.Duration
}

type ServerInfo struct {
	Hostname    string `json:"hostname"`
	IP          string `json:"ip"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Score       int    `json:"score"`
	Ping        string `json:"ping"`
}

type StatusInfo struct {
	Connected   bool   `json:"connected"`
	Server      string `json:"server"`
	Country     string `json:"country"`
	IP          string `json:"ip"`
	ConnectedAt int64  `json:"connected_at"`
}

type PingResult struct {
	Connected bool   `json:"connected"`
	Direct    bool   `json:"direct"`
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

type Provider interface {
	Start(ctx context.Context) error
	Rotate() error
	ConnectTo(hostname string) error
	ListServers() ([]ServerInfo, error)
	RefreshServers() ([]ServerInfo, error)
	Status() (StatusInfo, error)
	Ping() (PingResult, error)
	CurrentIP() string
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
func (d *directProvider) Close() error                                        { return nil }

// stubs for OS providers to allow compilation before Task 2
func newLinuxProvider(cfg ProviderConfig) Provider   { return &stubProvider{cfg: cfg, os: "linux"} }
func newDarwinProvider(cfg ProviderConfig) Provider  { return &stubProvider{cfg: cfg, os: "darwin"} }
func newWindowsProvider(cfg ProviderConfig) Provider { return &stubProvider{cfg: cfg, os: "windows"} }

type stubProvider struct {
	cfg ProviderConfig
	os  string
}

func (s *stubProvider) Start(ctx context.Context) error                     { return nil }
func (s *stubProvider) Rotate() error                                       { return nil }
func (s *stubProvider) ConnectTo(string) error                              { return nil }
func (s *stubProvider) ListServers() ([]ServerInfo, error)                  { return nil, nil }
func (s *stubProvider) RefreshServers() ([]ServerInfo, error)               { return nil, nil }
func (s *stubProvider) Status() (StatusInfo, error)                         { return StatusInfo{}, nil }
func (s *stubProvider) Ping() (PingResult, error)                           { return PingResult{Direct: s.os == "windows"}, nil }
func (s *stubProvider) CurrentIP() string                                   { return "" }
func (s *stubProvider) Close() error                                        { return nil }
