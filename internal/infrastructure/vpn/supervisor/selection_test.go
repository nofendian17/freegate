package supervisor

import (
	"testing"

	"github.com/davegallant/vpngate/pkg/vpn"
)

func TestPickWeightedBiasesTowardGoodServers(t *testing.T) {
	candidates := []vpn.Server{
		{HostName: "best", Score: 1_000_000, Ping: "10"},
		{HostName: "same-score-slow", Score: 1_000_000, Ping: "900"},
		{HostName: "worst", Score: 100, Ping: "50"},
	}
	best, worst := 0, 0
	for i := 0; i < 2000; i++ {
		switch pickWeighted(candidates).HostName {
		case "best":
			best++
		case "worst":
			worst++
		}
	}
	if best <= worst {
		t.Fatalf("expected the best server to be picked more often than the worst: best=%d worst=%d", best, worst)
	}
	if best == 2000 {
		t.Fatal("selection was not random: the best server was always picked")
	}
}

func TestPickWeightedPingBreaksScoreTie(t *testing.T) {
	// Equal scores, different pings: the low-ping server must win out.
	candidates := []vpn.Server{
		{HostName: "slow", Score: 500_000, Ping: "800"},
		{HostName: "fast", Score: 500_000, Ping: "20"},
	}
	fast, slow := 0, 0
	for i := 0; i < 2000; i++ {
		switch pickWeighted(candidates).HostName {
		case "fast":
			fast++
		case "slow":
			slow++
		}
	}
	if fast <= slow {
		t.Fatalf("expected the low-ping server to be picked more often: fast=%d slow=%d", fast, slow)
	}
}

func TestPickWeightedSingleCandidate(t *testing.T) {
	got := pickWeighted([]vpn.Server{{HostName: "only", Score: 1, Ping: ""}})
	if got.HostName != "only" {
		t.Fatalf("expected the only candidate, got %q", got.HostName)
	}
}

func TestSelectionWeightPingNeutral(t *testing.T) {
	// Ping "" / "0" means "unknown", not "great": it must not earn the
	// full bonus a real low ping would, and must not be penalized like a
	// real high ping. It should sit strictly between the slowest and
	// fastest known-ping candidates.
	slow := selectionWeight(vpn.Server{Score: 100, Ping: "500"}, 100, 500)
	unknown := selectionWeight(vpn.Server{Score: 100, Ping: ""}, 100, 500)
	zero := selectionWeight(vpn.Server{Score: 100, Ping: "0"}, 100, 500)
	fast := selectionWeight(vpn.Server{Score: 100, Ping: "50"}, 100, 500)

	if unknown != zero {
		t.Fatalf("ping missing (%v) and ping 0 (%v) must be treated the same", unknown, zero)
	}
	if !(slow < unknown && unknown < fast) {
		t.Fatalf("expected slow(%v) < unknown(%v) < fast(%v)", slow, unknown, fast)
	}
}

func TestSelectionWeightMinFloor(t *testing.T) {
	// A candidate far below the best keeps a minimum weight so it stays
	// eligible for selection, preserving exit-IP variety.
	w := selectionWeight(vpn.Server{Score: 1, Ping: "999"}, 1_000_000, 1000)
	if w < selectionMinWeight {
		t.Fatalf("expected weight floored at %v, got %v", selectionMinWeight, w)
	}
}

func TestMatchCountryExclusion(t *testing.T) {
	jp := vpn.Server{CountryLong: "Japan", CountryShort: "JP"}
	kr := vpn.Server{CountryLong: "Korea Republic of", CountryShort: "KR"}

	if matchCountry("!Japan", jp) {
		t.Error("!Japan should exclude Japan")
	}
	if !matchCountry("!Japan", kr) {
		t.Error("!Japan should allow Korea")
	}
	if !matchCountry("", jp) {
		t.Error("empty filter should allow everything")
	}
	if !matchCountry("!", jp) {
		t.Error("bare ! should behave like empty filter")
	}
	if !matchCountry("!Japan", vpn.Server{CountryLong: "United States", CountryShort: "US"}) {
		t.Error("!Japan should allow United States")
	}
}
