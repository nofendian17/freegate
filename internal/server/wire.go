package server

import (
	"net/http"

	"freegate/internal/config"
	"freegate/internal/infrastructure/upstream"
)

// buildSharedTransport creates a single tuned transport shared by all upstreams.
func buildSharedTransport(dialer *upstream.Dialer) *http.Transport {
	return upstream.NewTransport(dialer)
}

// buildUpstreams creates the three upstream clients sharing the same transport.
func buildUpstreams(cfg *config.Config, tr *http.Transport) (*upstream.OpenCodeUpstream, *upstream.KiloUpstream, *upstream.LLM7Upstream) {
	opencode := upstream.NewOpenCodeUpstreamWithTransport(
		cfg.UpstreamURLOpenCode, cfg.UpstreamKeyOpenCode, tr, cfg.UpstreamOpenCodeFreeAllowlist,
	)
	kilo := upstream.NewKiloUpstreamWithTransport(cfg.UpstreamURLKilo, cfg.UpstreamKeyKilo, tr)
	llm7 := upstream.NewLLM7UpstreamWithTransport(cfg.UpstreamURLLLM7, tr)
	return opencode, kilo, llm7
}

// buildUpstreamsAndRouter is exposed for Server struct initialization.
func buildUpstreamsAndRouter(cfg *config.Config, tr *http.Transport) (*upstream.OpenCodeUpstream, *upstream.KiloUpstream, *upstream.LLM7Upstream, *upstream.Router) {
	opencode, kilo, llm7 := buildUpstreams(cfg, tr)
	router := upstream.NewRouter(opencode, kilo, llm7)
	return opencode, kilo, llm7, router
}
