package vpn

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/davegallant/vpngate/pkg/vpn"
)

// serverRegistry caches VPNGate server list and applies filtering/weighted selection.
// Extracted from supervisorProvider to separate concerns (SRP).
type serverRegistry struct {
	mu          sync.Mutex
	servers     []vpn.Server
	lastRefresh time.Time
	cfg         ProviderConfig
}

func newServerRegistry(cfg ProviderConfig) *serverRegistry {
	if cfg.RefreshInt == 0 {
		cfg.RefreshInt = 300 * time.Second
	}
	return &serverRegistry{cfg: cfg}
}

func (r *serverRegistry) matches(sv vpn.Server) bool {
	if !matchCountry(r.cfg.Country, sv) {
		return false
	}
	if r.cfg.MinScore > 0 && sv.Score < r.cfg.MinScore {
		return false
	}
	if r.cfg.MaxPing > 0 {
		if p, ok := parsePing(sv.Ping); !ok || p > r.cfg.MaxPing {
			return false
		}
	}
	return true
}

func (r *serverRegistry) getServers() ([]vpn.Server, error) {
	r.mu.Lock()
	refresh := r.lastRefresh.IsZero() || time.Since(r.lastRefresh) >= r.cfg.RefreshInt
	servers := r.servers
	r.mu.Unlock()
	if refresh || len(servers) == 0 {
		list, err := fetchServerList(refresh)
		if err != nil {
			if len(servers) == 0 {
				return nil, fmt.Errorf("fetch vpngate server list: %w", err)
			}
			slog.Warn("vpn: server list refresh failed, using stale cache", "error", err)
		} else {
			servers = *list
			r.mu.Lock()
			r.servers = servers
			r.lastRefresh = time.Now()
			r.mu.Unlock()
		}
	}
	return servers, nil
}

func (r *serverRegistry) listServers() ([]ServerInfo, error) {
	list, err := r.getServers()
	if err != nil {
		return nil, err
	}
	out := make([]ServerInfo, 0, len(list))
	for _, sv := range list {
		if !r.matches(sv) {
			continue
		}
		out = append(out, ServerInfo{
			Hostname: sv.HostName, IP: sv.IPAddr,
			Country: sv.CountryLong, CountryCode: sv.CountryShort,
			Score: sv.Score, Ping: sv.Ping,
		})
	}
	return out, nil
}

func (r *serverRegistry) refreshServers() ([]ServerInfo, error) {
	list, err := fetchServerList(true)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.servers = *list
	r.lastRefresh = time.Now()
	r.mu.Unlock()
	out := make([]ServerInfo, 0, len(*list))
	for _, sv := range *list {
		out = append(out, ServerInfo{
			Hostname: sv.HostName, IP: sv.IPAddr,
			Country: sv.CountryLong, CountryCode: sv.CountryShort,
			Score: sv.Score, Ping: sv.Ping,
		})
	}
	return out, nil
}

func (r *serverRegistry) pickServer(tried map[string]bool) (vpn.Server, error) {
	servers, err := r.getServers()
	if err != nil {
		return vpn.Server{}, err
	}
	var candidates []vpn.Server
	for _, sv := range servers {
		if tried[sv.HostName] {
			continue
		}
		if !r.matches(sv) {
			continue
		}
		candidates = append(candidates, sv)
	}
	if len(candidates) == 0 {
		return vpn.Server{}, fmt.Errorf("no vpngate servers match the filters")
	}
	return pickWeighted(candidates), nil
}
