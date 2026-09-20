// Package server wires the application together: it constructs all
// collaborators, builds the HTTP router, and owns the http.Server
// lifecycle (start, graceful shutdown).
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"freegate/internal/application"
	"freegate/internal/config"
	"freegate/internal/delivery/admin"
	"freegate/internal/delivery/handler"
	"freegate/internal/delivery/middleware"
	"freegate/internal/delivery/ui"
	"freegate/internal/domain"
	"freegate/internal/httputil"
	"freegate/internal/infrastructure/metrics"
	"freegate/internal/infrastructure/providers"
	"freegate/internal/infrastructure/recorder"
	"freegate/internal/infrastructure/upstream"
	"freegate/web"
)

const (
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 30 * time.Second
	serverIdleTimeout       = 120 * time.Second
	shutdownTimeout         = 10 * time.Second
)

// Server owns the freegate HTTP server: configuration, dependencies,
// and lifecycle. Build it with New, then call Run.
type Server struct {
	cfg         *config.Config
	httpSrv     *http.Server
	Handler     http.Handler
	logger      *slog.Logger
	opencode    *upstream.OpenCodeUpstream
	kilo        *upstream.KiloUpstream
	llm7        *upstream.LLM7Upstream
	pstore      *providers.Store
	manager     *upstream.ProviderManager
	combo       *upstream.ComboRouter
	rec         *recorder.Recorder
	rateLimit   *middleware.RateLimiter
	wg          sync.WaitGroup // tracks background workers
}

// upstreamToDomain flattens custom providers into the domain upstream list.

func upstreamToDomain(all []*upstream.CustomUpstream) []domain.Upstream {
	out := make([]domain.Upstream, 0, len(all))
	for _, u := range all {
		if u == nil {
			continue
		}
		out = append(out, u)
	}
	return out
}

func comboRows(pstore *providers.Store) ([]upstream.ComboTierRow, error) {
	rows, err := pstore.ListCombos()
	if err != nil {
		return nil, err
	}
	out := make([]upstream.ComboTierRow, 0, len(rows))
	for _, c := range rows {
		out = append(out, upstream.ComboTierRow{Name: c.Name, Tiers: c.Tiers})
	}
	return out, nil
}

// syncRelayPools loads enabled proxy pools into the shared edge-relay
// selector so upstream requests route via the Vercel relay.
func syncRelayPools(pstore *providers.Store) {
	pools, err := pstore.ListPools()
	if err != nil {
		return
	}
	rp := make([]upstream.RelayPool, 0, len(pools))
	for _, p := range pools {
		if !p.Enabled {
			continue
		}
		rp = append(rp, upstream.RelayPool{URL: p.ProxyURL, NoProxy: p.NoProxy})
	}
	upstream.SharedRelay.SetPools(rp)
}

// New constructs a Server from configuration. It wires all
// dependencies (VPN, upstreams, application services, recorder, UI,
// HTTP router) but does not start listening or background workers.
// Use Run for that.
func New(cfg *config.Config) (*Server, error) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	}))
	slog.SetDefault(logger)

	// Client-IP derivation: honor X-Forwarded-For / X-Real-IP only when
	// explicitly deployed behind a trusted reverse proxy.
	httputil.SetTrustProxyHeaders(cfg.TrustProxyHeaders)

	// One shared transport routes all upstreams. Sharing the Transport
	// pools idle connections once instead of per-upstream.
	sharedTr := buildSharedTransport()

	opencode, kilo, llm7, infraRouter := buildUpstreamsAndRouter(cfg, sharedTr)

	pstore, err := providers.Open(cfg.ProvidersDBPath)
	if err != nil {
		return nil, fmt.Errorf("open providers db: %w", err)
	}
	mgr := upstream.NewProviderManager(pstore, sharedTr)
	if err := mgr.Rebuild(); err != nil {
		logger.Warn("custom providers rebuild failed, keeping legacy", "error", err)
	}
	syncRelayPools(pstore)
	combo := upstream.NewComboRouter(infraRouter)
	lookup := func(name string) domain.Upstream {
		switch name {
		case "opencode":
			return opencode
		case "kilo":
			return kilo
		case "llm7":
			return llm7
		default:
			if strings.HasPrefix(name, "custom:") {
				for _, u := range mgr.All() {
					if u.Name() == name {
						return u
					}
				}
			}
			return nil
		}
	}
	rows, err := comboRows(pstore)
	if err != nil {
		logger.Warn("combo rows load failed, keeping empty registry", "error", err)
	} else {
		combo.SetCustoms(upstreamToDomain(mgr.All()))
		combo.RebuildCombos(rows, lookup)
	}

	m := metrics.New()
	ms := application.NewModelService(combo)
	rec := recorder.NewRecorderWithDeps(recorder.Deps{
		Metrics: m.Snapshot,
		Models:  ms.AllModels,
	})
	cs := application.NewChatService(combo, m)
	cs.WithRequestLogger(rec.RecordRequestLog)
	// Raw upstream response logging via slog (stdout), for diagnosing
	// degenerate upstream behavior. Off unless UPSTREAM_CAPTURE=true.
	if cfg.UpstreamCapture {
		cs.WithRawUpstreamLog(true)
	}

	tpl, err := ui.LoadTemplates(web.Templates())
	if err != nil {
		return nil, fmt.Errorf("load UI templates: %w", err)
	}

	uiHandler := ui.New(rec, tpl, web.Static(), cfg.AdminToken)
	// Direct config for Responses models (e.g. muse-spark) and Messages
	// models (e.g. union-alpha via /zen/v1/messages per 9router PR #4111).
	handler.SetResponseModels(cfg.ResponseModels)
	handler.SetMessageModels(cfg.MessageModels)
	apiHandler := handler.New(cs, ms, m)
	rl := middleware.NewRateLimiter(cfg.RateLimit)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.CORS)

	apiAuth := middleware.ApiAuth(cfg.APIKey, cfg.AdminToken)
	adminAuth := middleware.AdminAuth(cfg.AdminToken)

	// Public routes — must be before admin mount so they are not shadowed.
	r.Get("/login", uiHandler.LoginPage)
	r.Post("/login", uiHandler.Login)
	r.Post("/logout", uiHandler.Logout)
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(web.Static()))))
	// /ready is public: Docker HEALTHCHECK and ops probes have no token.
	// It only reveals models-loaded state, same class as /api/health.
	r.With(rl.Middleware).Get("/ready", apiHandler.Ready)

	rebuild := func() error {
		if err := mgr.Rebuild(); err != nil {
			return err
		}
		syncRelayPools(pstore)
		rows, err := comboRows(pstore)
		if err != nil {
			return err
		}
		combo.SetCustoms(upstreamToDomain(mgr.All()))
		combo.RebuildCombos(rows, lookup)
		return nil
	}
	adminHandler := admin.New(pstore, rebuild, sharedTr).WithWarmer(mgr.Warm)

	// Dashboard + provider/combo management (admin-only).
	r.Group(func(r chi.Router) {
		r.Use(adminAuth)
		r.Mount("/", uiHandler.Routes())
		adminHandler.Register(r)
	})

	// API (OpenAI-compatible) — rate limit + auth apply to these only.
	r.With(rl.Middleware, apiAuth).Route("/v1", func(r chi.Router) {
		r.Get("/models", apiHandler.ListModels)
		r.Get("/metrics", apiHandler.Metrics)
		r.Post("/chat/completions", apiHandler.Chat)
		r.Post("/responses", apiHandler.Chat)
		r.Post("/messages", apiHandler.Chat)
	})

	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      0,
		IdleTimeout:       serverIdleTimeout,
	}

	return &Server{
		cfg:         cfg,
		httpSrv:     httpSrv,
		Handler:     r,
		logger:      logger,
		opencode:    opencode,
		kilo:        kilo,
		llm7:        llm7,
		pstore:      pstore,
		manager:     mgr,
		combo:       combo,
		rec:         rec,
		rateLimit:   rl,
	}, nil
}

// Run starts background workers (upstream refreshers, recorder sampler)
// and ListenAndServe. It blocks until ctx is canceled,
// then performs a graceful shutdown.
func (s *Server) Run(ctx context.Context) error {
	bgCtx, cancelBG := context.WithCancel(context.Background())
	defer cancelBG()

	// Background workers
	s.wg.Add(4)
	go func() {
		defer s.wg.Done()
		s.opencode.Start(bgCtx, time.Duration(s.cfg.UpstreamRefreshOpenCode)*time.Second)
	}()
	go func() {
		defer s.wg.Done()
		s.kilo.Start(bgCtx, time.Duration(s.cfg.UpstreamRefreshKilo)*time.Second)
	}()
	go func() {
		defer s.wg.Done()
		s.llm7.Start(bgCtx, time.Duration(s.cfg.UpstreamRefreshLLM7)*time.Second)
	}()
	go func() {
		defer s.wg.Done()
		s.rec.Start(bgCtx)
	}()
	s.manager.Start(bgCtx)

	s.logger.Info("starting server", "addr", s.httpSrv.Addr)
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		s.logger.Info("shutting down server...")
	case err := <-errCh:
		if err != nil {
			cancelBG()
			s.manager.Stop()
			_ = s.pstore.Close()
			s.wg.Wait()
			s.rateLimit.Stop()
			return fmt.Errorf("server failed: %w", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	// Signal background workers to stop
	cancelBG()
	s.manager.Stop()

	// Wait for all background workers to finish
	// But first wait for HTTP server to shut down
	if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
		s.logger.Error("server forced to shutdown", "error", err)
		_ = s.pstore.Close()
		s.rateLimit.Stop()
		return err
	}

	// Wait for background workers to complete
	s.wg.Wait()

	_ = s.pstore.Close()
	s.rateLimit.Stop()

	s.logger.Info("server stopped gracefully")
	return nil
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
