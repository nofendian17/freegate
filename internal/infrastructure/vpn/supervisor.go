package vpn

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/davegallant/vpngate/pkg/vpn"
)

const (
	selectionScoreWeight = 0.7
	selectionPingWeight  = 0.3
	selectionMinWeight   = 0.05
	rotateAttempts       = 3
	tunnelWaitTimeout    = 8 * time.Second
	ipCheckTimeout       = 6 * time.Second
	ipRefreshAttempts    = 3
	ipRefreshRetryDelay  = 3 * time.Second
	listFetchTimeout     = 20 * time.Second
)

// pickWeighted chooses a server via weighted random selection.
func pickWeighted(candidates []vpn.Server) vpn.Server {
	maxScore, maxPing := 0, 0
	for _, sv := range candidates {
		if sv.Score > maxScore {
			maxScore = sv.Score
		}
		if p, ok := parsePing(sv.Ping); ok && p > maxPing {
			maxPing = p
		}
	}

	weights := make([]float64, len(candidates))
	var total float64
	for i, sv := range candidates {
		w := selectionWeight(sv, maxScore, maxPing)
		weights[i] = w
		total += w
	}

	r := rand.Float64() * total
	for i, w := range weights {
		r -= w
		if r <= 0 {
			return candidates[i]
		}
	}
	return candidates[len(candidates)-1]
}

func selectionWeight(sv vpn.Server, maxScore, maxPing int) float64 {
	scoreW := 1.0
	if maxScore > 0 {
		scoreW = float64(sv.Score) / float64(maxScore)
	}
	pingW := 0.5
	if p, ok := parsePing(sv.Ping); ok && p > 0 && maxPing > 0 {
		pingW = 1 - float64(p)/float64(maxPing)
	}
	w := selectionScoreWeight*scoreW + selectionPingWeight*pingW
	if w < selectionMinWeight {
		w = selectionMinWeight
	}
	return w
}

func matchCountry(filter string, s vpn.Server) bool {
	if filter == "" {
		return true
	}
	f := strings.ToLower(strings.TrimSpace(filter))
	if strings.HasPrefix(f, "!") {
		excl := strings.TrimSpace(f[1:])
		if excl == "" {
			return true
		}
		return !strings.Contains(strings.ToLower(s.CountryLong), excl) &&
			!strings.EqualFold(s.CountryShort, excl)
	}
	return strings.Contains(strings.ToLower(s.CountryLong), f) ||
		strings.EqualFold(s.CountryShort, f)
}

func parsePing(ping string) (int, bool) {
	if ping == "" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(ping))
	return n, err == nil
}

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
