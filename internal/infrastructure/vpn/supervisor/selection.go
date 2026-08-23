package supervisor

import (
	"math/rand"
	"strconv"
	"strings"

	"github.com/davegallant/vpngate/pkg/vpn"
)

const (
	// Server-selection weighting. Score is the primary reliability signal;
	// ping is secondary (many relays report 0 or no ping at all).
	selectionScoreWeight = 0.7
	selectionPingWeight  = 0.3
	// selectionMinWeight keeps every filtered candidate eligible even when
	// its score is far below the best one, preserving exit-IP variety.
	selectionMinWeight = 0.05
)

// pickWeighted chooses a server via weighted random selection. Weights
// combine score and ping, each normalized across the candidate set: a
// higher score or a lower ping raises the odds, but every candidate stays
// eligible so consecutive rotations still vary the exit IP.
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

// selectionWeight computes the weight for one candidate given the max
// score and max ping across the candidate set. A higher score or a lower
// ping raises the weight; ping 0 or missing means "unknown" and gets a
// neutral weight, never the bonus a real low ping would earn.
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

// matchCountry reports whether a server matches the country filter. A
// leading "!" turns the filter into an exclusion, e.g. "!Japan" matches
// every country except Japan.
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
