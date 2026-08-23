package supervisor

import (
	"testing"

	"github.com/davegallant/vpngate/pkg/vpn"
)

func TestRegistryMatchesFilters(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		sv   vpn.Server
		want bool
	}{
		{"no filters passes everything", Config{}, vpn.Server{CountryLong: "Japan", CountryShort: "JP", Score: 1, Ping: "999"}, true},
		{"country include", Config{Country: "japan"}, vpn.Server{CountryLong: "Japan", CountryShort: "JP"}, true},
		{"country exclude", Config{Country: "!JP"}, vpn.Server{CountryLong: "Japan", CountryShort: "JP"}, false},
		{"min score rejects below", Config{MinScore: 100}, vpn.Server{Score: 99}, false},
		{"min score accepts at", Config{MinScore: 100}, vpn.Server{Score: 100}, true},
		{"max ping rejects above", Config{MaxPing: 200}, vpn.Server{Ping: "201"}, false},
		{"max ping rejects unparseable", Config{MaxPing: 200}, vpn.Server{Ping: ""}, false},
		{"max ping accepts at", Config{MaxPing: 200}, vpn.Server{Ping: "200"}, true},
		{"all filters combined", Config{Country: "!JP", MinScore: 10, MaxPing: 300}, vpn.Server{CountryLong: "Korea", CountryShort: "KR", Score: 11, Ping: "299"}, true},
	}
	r := &serverRegistry{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r.cfg = tt.cfg
			if got := r.matches(tt.sv); got != tt.want {
				t.Fatalf("matches() = %v, want %v (cfg=%+v sv=%+v)", got, tt.want, tt.cfg, tt.sv)
			}
		})
	}
}
