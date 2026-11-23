package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"freegate/internal/config"
	"freegate/internal/infrastructure/egress"
	"freegate/internal/infrastructure/registry"
	"freegate/internal/infrastructure/upstream"
)

// buildSharedTransport creates a single tuned transport shared by all upstreams.
func buildSharedTransport() *http.Transport {
	return upstream.NewTransport()
}

// buildEgress returns the env-configured default WARP pool when
// EGRESS_MODE=warp, or nil for direct connections. A malformed pool is a
// startup error: silently falling back to the host's own IP would defeat the
// reason the operator enabled WARP.
func buildEgress(cfg *config.Config) (*egress.Pool, error) {
	if cfg.EgressMode != "warp" {
		return nil, nil
	}
	opts, err := egress.ParseConfig(cfg.EgressStack, cfg.EgressPoolSize, cfg.EgressSourceIPv6)
	if err != nil {
		return nil, fmt.Errorf("WARP config: %w", err)
	}
	opts.FallbackDirect = cfg.EgressFallbackDirect
	opts.RegistrationTimeout = cfg.EgressRegistrationTimeout
	opts.RotateWait = cfg.EgressRotateWait
	pool, err := egress.NewPool(context.Background(), slog.Default(), opts)
	if err != nil {
		return nil, fmt.Errorf("WARP pool: %w", err)
	}
	slog.Info("egress: WARP pool enabled", "size", opts.Size, "stack", opts.Stack,
		"source", opts.SourceIPv6, "fallback_direct", opts.FallbackDirect)
	return pool, nil
}

// buildUpstreams creates the three upstream clients sharing the same transport.
// The returned router resolves every WARP pool at dial time, so persisted pool
// edits apply without rebuilding an upstream.
func buildUpstreams(cfg *config.Config, tr *http.Transport) (*upstream.OpenCodeUpstream, *upstream.KiloUpstream, *upstream.LLM7Upstream, *egress.Router, error) {
	pool, err := buildEgress(cfg)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	router := egress.NewRouter()
	router.SetDefault(pool)
	opencode := upstream.NewOpenCodeUpstreamWithEgress(
		cfg.UpstreamURLOpenCode, cfg.UpstreamKeyOpenCode, tr, cfg.UpstreamOpenCodeFreeAllowlist, router,
	)
	// Share RESPONSE_MODELS / MESSAGE_MODELS routing config with the handler
	// so targetFormatForModel and buildURL agree on the endpoint.
	opencode.SetResponseModels(cfg.ResponseModels)
	opencode.SetMessageModels(cfg.MessageModels)
	kilo := upstream.NewKiloUpstreamWithEgress(cfg.UpstreamURLKilo, cfg.UpstreamKeyKilo, tr, router)
	llm7 := upstream.NewLLM7UpstreamWithEgress(cfg.UpstreamURLLLM7, tr, router)
	return opencode, kilo, llm7, router, nil
}

// buildUpstreamsAndRouter is exposed for Server struct initialization.
func buildUpstreamsAndRouter(cfg *config.Config, tr *http.Transport) (*upstream.OpenCodeUpstream, *upstream.KiloUpstream, *upstream.LLM7Upstream, *upstream.Router, *egress.Router, error) {
	opencode, kilo, llm7, router, err := buildUpstreams(cfg, tr)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	route := upstream.NewRouter(opencode, kilo, llm7)
	return opencode, kilo, llm7, route, router, nil
}

// applyEgressPools loads every enabled WARP pool from the store and installs
// it in the router. Unchanged pools keep their live rotation state.
func applyEgressPools(ctx context.Context, pstore *registry.Store, cfg *config.Config, router *egress.Router) error {
	rows, err := pstore.ListPools(ctx)
	if err != nil {
		return err
	}
	configs := make([]egress.PoolConfig, 0, len(rows))
	for _, row := range rows {
		if registry.NormalizePoolKind(row.Kind) != registry.PoolKindWarp || !row.Enabled {
			continue
		}
		opts, err := egress.ParseConfig(row.WarpStack, row.WarpPoolSize, row.WarpSourceIPv6)
		if err != nil {
			return fmt.Errorf("warp pool %q: %w", row.Name, err)
		}
		opts.FallbackDirect = cfg.EgressFallbackDirect
		opts.RegistrationTimeout = cfg.EgressRegistrationTimeout
		opts.RotateWait = cfg.EgressRotateWait
		configs = append(configs, egress.PoolConfig{
			ID:                  row.ID,
			Name:                row.Name,
			Stack:               opts.Stack,
			Size:                opts.Size,
			SourceIPv6:          opts.SourceIPv6,
			FallbackDirect:      opts.FallbackDirect,
			RegistrationTimeout: opts.RegistrationTimeout,
		})
	}
	return router.Apply(ctx, configs)
}
