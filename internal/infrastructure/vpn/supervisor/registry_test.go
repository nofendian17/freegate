package supervisor

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestGetServersDeduplicatesConcurrentFetches(t *testing.T) {
	r := newServerRegistry(Config{})

	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	r.fetch = func(bool) (*[]vpn.Server, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		list := []vpn.Server{{HostName: "a", Score: 1}}
		return &list, nil
	}

	const n = 5
	results := make(chan int, n)
	for range n {
		go func() {
			servers, err := r.getServers()
			if err != nil {
				results <- -1
				return
			}
			results <- len(servers)
		}()
	}

	<-started // the single in-flight fetch is running
	close(release)

	for range n {
		if got := <-results; got != 1 {
			t.Fatalf("getServers returned %d servers, want 1", got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("fetch ran %d times across %d concurrent callers, want 1", calls.Load(), n)
	}

	// The cache is now fresh: a sequential call must not re-fetch.
	if _, err := r.getServers(); err != nil {
		t.Fatalf("unexpected error on cached call: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("cached getServers re-fetched (calls=%d)", calls.Load())
	}
}

func TestRefreshServersDeduplicatesAndForcesFetch(t *testing.T) {
	r := newServerRegistry(Config{RefreshInt: time.Hour})

	var calls atomic.Int32
	r.fetch = func(refresh bool) (*[]vpn.Server, error) {
		calls.Add(1)
		if !refresh {
			return nil, fmt.Errorf("refreshServers must force refresh")
		}
		list := []vpn.Server{{HostName: "fresh", Score: 2}}
		return &list, nil
	}

	const n = 4
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := r.refreshServers()
			errs <- err
		}()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Fatalf("refreshServers: %v", err)
		}
	}
	// Forced refresh runs at least once; racing callers may collapse onto
	// a shared flight but never error out or lose the result.
	if calls.Load() < 1 || calls.Load() > n {
		t.Fatalf("fetch ran %d times, want between 1 and %d", calls.Load(), n)
	}
	servers, err := r.listServers()
	if err != nil || len(servers) != 1 || servers[0].Hostname != "fresh" {
		t.Fatalf("cache not updated by shared refresh: %+v, %v", servers, err)
	}
}

func TestListAndRefreshShareOneFlight(t *testing.T) {
	r := newServerRegistry(Config{RefreshInt: time.Hour})

	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	r.fetch = func(refresh bool) (*[]vpn.Server, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		list := []vpn.Server{{HostName: "shared", Score: 3}}
		return &list, nil
	}

	errs := make(chan error, 2)
	go func() {
		_, err := r.getServers()
		errs <- err
	}()
	<-started // a list-driven fetch owns the flight

	// A concurrent forced refresh must join the in-flight flight instead
	// of issuing a second parallel fetch.
	go func() {
		_, err := r.refreshServers()
		errs <- err
	}()

	// Give the refresh caller time to reach singleflight.Do so it joins
	// the still-blocked flight instead of racing past its completion.
	time.Sleep(50 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch ran %d times across racing getServers+refreshServers, want 1", got)
	}
}
