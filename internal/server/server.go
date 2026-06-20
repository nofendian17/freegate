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
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"freegate/internal/application"
	"freegate/internal/config"
	"freegate/internal/delivery/handler"
	"freegate/internal/delivery/middleware"
	"freegate/internal/delivery/ui"
	"freegate/internal/domain"
	"freegate/internal/infrastructure/metrics"
	"freegate/internal/infrastructure/recorder"
	"freegate/internal/infrastructure/tor"
	"freegate/internal/infrastructure/upstream"
	"freegate/web"
)

const (
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 30 * time.Second
	serverIdleTimeout       = 120 * time.Second
	shutdownTimeout         = 10 * time.Second
	torMonitorInterval      = 5 * time.Minute
	defaultMaxRetries       = 5
	defaultRetryDelay       = 3 * time.Second
)

// Server owns the freegate HTTP server: configuration, dependencies,
// and lifecycle. Build it with New, then call Run.
type Server struct {
	cfg       *config.Config
	httpSrv   *http.Server
	logger    *slog.Logger
	tc        *tor.Controller
	opencode  *upstream.OpenCodeUpstream
	kilo      *upstream.KiloUpstream
	mimo      *upstream.MimoFreeUpstream
	rec       *recorder.Recorder
	rateLimit *middleware.RateLimiter
	wg        sync.WaitGroup // tracks background workers
}

// routerAdapter wraps *upstream.Router to satisfy application.Router,
// whose Select returns (domain.Upstream, error).
type routerAdapter struct {
	*upstream.Router
}

func (a *routerAdapter) Select(modelID string) (domain.Upstream, error) {
	return &upstreamAdapter{Upstream: a.Router.Select(modelID)}, nil
}

// upstreamAdapter wraps upstream.Upstream to satisfy domain.Upstream
// (different ChatRequest signature, different Start signature).
type upstreamAdapter struct {
	upstream.Upstream
}

func (u *upstreamAdapter) ChatCompletion(ctx context.Context, req domain.ChatRequest) (*http.Response, error) {
	return u.Upstream.ChatCompletion(ctx, req.Body)
}

func (u *upstreamAdapter) Start(ctx context.Context) {
	u.Upstream.Start(ctx, 0)
}

// New constructs a Server from configuration. It wires all
// dependencies (Tor, upstreams, application services, recorder, UI,
// HTTP router) but does not start listening or background workers.
// Use Run for that.
func New(cfg *config.Config) (*Server, error) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	}))
	slog.SetDefault(logger)

	tc := tor.NewController(cfg.TorHost, cfg.CtrlPort, cfg.CtrlPass, cfg.SOCKSAddr)

	socks := cfg.SOCKSAddr
	if cfg.BypassProxy {
		socks = ""
		slog.Info("proxy bypass enabled: direct connections, no IP rotation")
	}

	opencode := upstream.NewOpenCodeUpstream(
		cfg.UpstreamURLOpenCode,
		cfg.UpstreamKeyOpenCode,
		socks,
		cfg.UpstreamOpenCodeFreeAllowlist,
	)
	kilo := upstream.NewKiloUpstream(
		cfg.UpstreamURLKilo,
		cfg.UpstreamKeyKilo,
		socks,
	)

	mimo := upstream.NewMimoFreeUpstream(
		cfg.UpstreamURLMimo,
		socks,
	)

	infraRouter := upstream.NewRouter(opencode, kilo, mimo)
	appRouter := &routerAdapter{Router: infraRouter}

	m := metrics.New()

	cs := application.NewChatService(appRouter, tc, m, defaultMaxRetries, defaultRetryDelay)
	if cfg.BypassProxy {
		cs = application.NewChatService(appRouter, nil, m, defaultMaxRetries, defaultRetryDelay)
	}
	ms := application.NewModelService(infraRouter)

	rec := recorder.NewRecorder(m.Snapshot)
	rec.SetModelsFunc(ms.AllModels)
	rec.SetTorIPFunc(func() string {
		if cfg.BypassProxy {
			return "direct"
		}
		return tc.CurrentIP()
	})
	cs.WithRequestLogger(rec.RecordRequestLog)

	tpl, err := ui.LoadTemplates(web.Templates())
	if err != nil {
		return nil, fmt.Errorf("load UI templates: %w", err)
	}

	uiHandler := ui.NewHandler(rec, tpl, web.Static())
	apiHandler := handler.New(cs, ms, m)
	rl := middleware.NewRateLimiter(cfg.RateLimit)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.CORS)

	// UI dashboard at / — no rate limit, no auth
	r.Mount("/", uiHandler.Routes())

	// API (OpenAI-compatible) — rate limit + auth apply to these only.
	// These specific routes are registered on the root mux and are checked
	// BEFORE the default handler set by Mount("/").
	r.With(rl.Middleware, middleware.Auth(cfg.APIKey)).Route("/v1", func(r chi.Router) {
		r.Get("/models", apiHandler.ListModels)
		r.Get("/metrics", apiHandler.Metrics)
		r.Post("/chat/completions", apiHandler.Chat)
	})
	r.With(rl.Middleware, middleware.Auth(cfg.APIKey)).Post("/v1/messages", apiHandler.Chat)
	r.With(rl.Middleware, middleware.Auth(cfg.APIKey)).Get("/ready", apiHandler.Ready)

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
		cfg:       cfg,
		httpSrv:   httpSrv,
		logger:    logger,
		tc:        tc,
		opencode:  opencode,
		kilo:      kilo,
		mimo:      mimo,
		rec:       rec,
		rateLimit: rl,
	}, nil
}

// Run starts background workers (upstream refreshers, Tor IP monitor,
// recorder sampler) and ListenAndServe. It blocks until ctx is canceled,
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
		s.mimo.Start(bgCtx, time.Duration(s.cfg.UpstreamRefreshMimo)*time.Second)
	}()
	go func() {
		defer s.wg.Done()
		s.rec.Start(bgCtx)
	}()

	stopIP := make(chan struct{})
	if !s.cfg.BypassProxy {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.tc.StartMonitor(torMonitorInterval, stopIP)
		}()
	} else {
		slog.Info("tor: IP monitor skipped (bypass enabled)")
	}

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
			close(stopIP)
			s.wg.Wait()
			s.tc.Close()
			s.rateLimit.Stop()
			return fmt.Errorf("server failed: %w", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	// Signal background workers to stop
	cancelBG()
	close(stopIP)

	// Wait for all background workers to finish
	// But first wait for HTTP server to shut down
	if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
		s.logger.Error("server forced to shutdown", "error", err)
		s.tc.Close()
		s.rateLimit.Stop()
		return err
	}

	// Wait for background workers to complete
	s.wg.Wait()

	s.tc.Close()
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
