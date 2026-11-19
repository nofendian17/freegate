package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"freegate/internal/config"
	"freegate/internal/infrastructure/egress"
	"freegate/internal/infrastructure/upstream"
)

// buildSharedTransport creates a single tuned transport shared by all upstreams.
func buildSharedTransport() *http.Transport {
	return upstream.NewTransport()
}

// buildEgress returns the WARP pool when EGRESS_MODE=warp, or nil for direct
// connections. A bad pool is a startup error: silently falling back to the
// host's own IP would defeat the reason the operator enabled WARP.
func buildEgress(cfg *config.Config) (*egress.Pool, error) {
	if cfg.EgressMode != "warp" {
		return nil, nil
	}
	endpoints, err := egress.ParsePool(cfg.EgressWarpPool)
	if err != nil {
		return nil, fmt.Errorf("EGRESS_WARP_POOL: %w", err)
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("EGRESS_MODE=warp requires a non-empty EGRESS_WARP_POOL")
	}
	pool := egress.NewPool(egress.Options{
		Endpoints:      endpoints,
		FallbackDirect: cfg.EgressFallbackDirect,
		RotateTimeout:  cfg.EgressRotateTimeout,
		FailureCool:    cfg.EgressCooldown,
		// A dial that arrives while its endpoint is rotating waits for the
		// rotation to finish, plus a margin, so the retry after a free-tier
		// limit does not race the reconnect.
		WaitReady: cfg.EgressRotateTimeout + 10*time.Second,
	})
	slog.Info("egress: WARP pool enabled", "endpoints", len(endpoints), "fallback_direct", cfg.EgressFallbackDirect)
	return pool, nil
}

// buildUpstreams creates the three upstream clients sharing the same transport.
func buildUpstreams(cfg *config.Config, tr *http.Transport) (*upstream.OpenCodeUpstream, *upstream.KiloUpstream, *upstream.LLM7Upstream, upstream.Egress, error) {
	pool, err := buildEgress(cfg)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// Keep the Egress interface truly nil when no pool is configured: a typed
	// nil *egress.Pool stored in the interface would defeat every `eg == nil`
	// fail-closed check downstream (see upstream.NewWarpTransport).
	var eg upstream.Egress
	if pool != nil {
		eg = pool
	}
	opencode := upstream.NewOpenCodeUpstreamWithEgress(
		cfg.UpstreamURLOpenCode, cfg.UpstreamKeyOpenCode, tr, cfg.UpstreamOpenCodeFreeAllowlist, eg,
	)
	// Share RESPONSE_MODELS / MESSAGE_MODELS routing config with the handler
	// so targetFormatForModel and buildURL agree on the endpoint.
	opencode.SetResponseModels(cfg.ResponseModels)
	opencode.SetMessageModels(cfg.MessageModels)
	kilo := upstream.NewKiloUpstreamWithEgress(cfg.UpstreamURLKilo, cfg.UpstreamKeyKilo, tr, eg)
	llm7 := upstream.NewLLM7UpstreamWithEgress(cfg.UpstreamURLLLM7, tr, eg)
	return opencode, kilo, llm7, eg, nil
}

// buildUpstreamsAndRouter is exposed for Server struct initialization.
func buildUpstreamsAndRouter(cfg *config.Config, tr *http.Transport) (*upstream.OpenCodeUpstream, *upstream.KiloUpstream, *upstream.LLM7Upstream, *upstream.Router, upstream.Egress, error) {
	opencode, kilo, llm7, eg, err := buildUpstreams(cfg, tr)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	router := upstream.NewRouter(opencode, kilo, llm7)
	return opencode, kilo, llm7, router, eg, nil
}
