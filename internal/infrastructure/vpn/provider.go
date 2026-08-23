// Package vpn provides the VPN abstraction used by the proxy server:
// a Provider interface with two implementations — a no-op direct
// provider, and an adapter around the shared tunnel core
// (internal/infrastructure/vpn/supervisor) that runs the OpenVPN tunnel,
// SOCKS5 proxy, and reconnect/refresh loops inside this process.
package vpn

import (
	"context"
	"time"

	"freegate/internal/infrastructure/vpn/supervisor"
	"freegate/internal/infrastructure/vpngate"
)

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
	core := supervisor.New(supervisor.Config{
		SocksAddr:  cfg.SocksAddr,
		Country:    cfg.Country,
		MinScore:   cfg.MinScore,
		MaxPing:    cfg.MaxPing,
		RefreshInt: cfg.RefreshInt,
	})
	return providerAdapter{core}, nil
}

// providerAdapter adapts the core's error-free Status/Ping signatures to
// the Provider interface (kept stable so server-side callers don't churn).
type providerAdapter struct {
	*supervisor.Supervisor
}

func (p providerAdapter) Status() (StatusInfo, error) { return p.Supervisor.Status(), nil }
func (p providerAdapter) Ping() (PingResult, error)   { return p.Supervisor.Ping(), nil }

type directProvider struct{}

func (d *directProvider) Start(ctx context.Context) error       { return nil }
func (d *directProvider) Rotate() error                         { return nil }
func (d *directProvider) ConnectTo(string) error                { return nil }
func (d *directProvider) ListServers() ([]ServerInfo, error)    { return nil, nil }
func (d *directProvider) RefreshServers() ([]ServerInfo, error) { return nil, nil }
func (d *directProvider) Status() (StatusInfo, error)           { return StatusInfo{}, nil }
func (d *directProvider) Ping() (PingResult, error)             { return PingResult{Direct: true}, nil }
func (d *directProvider) CurrentIP() string                     { return "direct" }
func (d *directProvider) InstallHint() string                   { return "" }
func (d *directProvider) Close() error                          { return nil }
