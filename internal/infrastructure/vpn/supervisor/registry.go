package supervisor

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/davegallant/vpngate/pkg/vpn"
)

// listFetchTimeout bounds a single server-list refresh so a hung upstream
// (the library retries internally for up to minutes) cannot stall a
// rotation.
const listFetchTimeout = 20 * time.Second

// serverRegistry caches the VPNGate server list and applies
// filtering/weighted selection.
type serverRegistry struct {
	mu          sync.Mutex
	servers     []vpn.Server
	lastRefresh time.Time
	cfg         Config
}

func newServerRegistry(cfg Config) *serverRegistry {
	return &serverRegistry{cfg: cfg}
}

// matches reports whether a server passes the configured country / score
// / ping filters.
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

// getServers returns the cached server list, refreshing it (via the
// vpngate library) when stale. A failed refresh keeps serving the stale
// cache instead of wedging.
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
			slog.Warn("vpngate: server list refresh failed, using stale cache", "error", err)
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

// listServers returns the filtered, dashboard-ready payload for /servers.
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
		out = append(out, serverInfoOf(sv))
	}
	return out, nil
}

// refreshServers forces a live re-fetch of the vpngate list (ignoring the
// refresh interval), swaps the cache, and returns the freshly filtered
// relays.
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
		out = append(out, serverInfoOf(sv))
	}
	return out, nil
}

// pickServer returns a weighted-random server matching the filters,
// excluding any hostnames in tried.
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

func serverInfoOf(sv vpn.Server) ServerInfo {
	return ServerInfo{
		Hostname: sv.HostName, IP: sv.IPAddr,
		Country: sv.CountryLong, CountryCode: sv.CountryShort,
		Score: sv.Score, Ping: sv.Ping,
	}
}

// fetchServerList wraps vpn.GetListWithOptions with a hard timeout so a
// hung upstream (the library retries internally for up to minutes) cannot
// stall a rotation.
func fetchServerList(refresh bool) (*[]vpn.Server, error) {
	type result struct {
		servers *[]vpn.Server
		err     error
	}
	ch := make(chan result, 1)
	go func() {
		list, err := vpn.GetListWithOptions("", "", vpn.ListOptions{Refresh: refresh})
		ch <- result{servers: list, err: err}
	}()
	select {
	case r := <-ch:
		return r.servers, r.err
	case <-time.After(listFetchTimeout):
		return nil, fmt.Errorf("server list fetch timed out after %s", listFetchTimeout)
	}
}
