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
	"freegate/internal/infrastructure/upstream"
	"freegate/internal/infrastructure/vpn"
	"freegate/internal/infrastructure/vpngate"
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
	logger      *slog.Logger
	vpnProvider vpn.Provider
	opencode    *upstream.OpenCodeUpstream
	kilo        *upstream.KiloUpstream
	llm7        *upstream.LLM7Upstream
	rec         *recorder.Recorder
	rateLimit   *middleware.RateLimiter
	wg          sync.WaitGroup // tracks background workers
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

// vpnUI adapts the VPN provider for the dashboard: it wraps the Provider
// (ListServers/ConnectTo/ForceNewIP/Status/Ping/CurrentIP) and adds the live
// direct/tunnel switch backed by the shared upstream dialer.
type vpnUI struct {
	provider vpn.Provider
	dialer   *upstream.Dialer
}

func (v *vpnUI) ListServers() ([]vpn.ServerInfo, error)   { return v.provider.ListServers() }
func (v *vpnUI) RefreshServers() ([]vpn.ServerInfo, error) { return v.provider.RefreshServers() }
func (v *vpnUI) ConnectTo(hostname string) error          { return v.provider.ConnectTo(hostname) }
func (v *vpnUI) ForceNewIP() error                        { return v.provider.Rotate() }
func (v *vpnUI) Status() (vpn.StatusInfo, error)          { return v.provider.Status() }
func (v *vpnUI) Ping() (vpn.PingResult, error)            { return v.provider.Ping() }
func (v *vpnUI) CurrentIP() string                        { return v.provider.CurrentIP() }
func (v *vpnUI) InstallHint() string                      { return v.provider.InstallHint() }

// sidecarAdapter wraps the legacy vpngate sidecar Controller so Docker
// deployments (VPNGATE_HOST=vpn) keep using the sidecar's HTTP API instead
// of the embedded per-OS provider. The sidecar already runs openvpn with
// CAP_NET_ADMIN, so no install hint is needed.
type sidecarAdapter struct {
	ctrl *vpngate.Controller
}

func (s *sidecarAdapter) Start(ctx context.Context) error {
	stop := make(chan struct{})
	go s.ctrl.StartMonitor(5*time.Minute, stop)
	<-ctx.Done()
	close(stop)
	return nil
}
func (s *sidecarAdapter) Rotate() error                             { return s.ctrl.ForceNewIP() }
func (s *sidecarAdapter) ConnectTo(h string) error                  { return s.ctrl.ConnectTo(h) }
func (s *sidecarAdapter) ListServers() ([]vpn.ServerInfo, error)    { return s.ctrl.ListServers() }
func (s *sidecarAdapter) RefreshServers() ([]vpn.ServerInfo, error) { return s.ctrl.RefreshServers() }
func (s *sidecarAdapter) Status() (vpn.StatusInfo, error)           { return s.ctrl.Status() }
func (s *sidecarAdapter) Ping() (vpn.PingResult, error)             { return s.ctrl.Ping() }
func (s *sidecarAdapter) CurrentIP() string                         { return s.ctrl.CurrentIP() }
func (s *sidecarAdapter) InstallHint() string                       { return "" }
func (s *sidecarAdapter) Close() error                              { s.ctrl.Close(); return nil }

func (v *vpnUI) SetDirect(direct bool) error {
	v.dialer.SetDirect(direct)
	return nil
}

func (v *vpnUI) Direct() bool {
	return v.dialer.IsDirect()
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

	var vpnProvider vpn.Provider
	// Docker sidecar mode: VPNGATE_HOST explicitly set to non-loopback (e.g. "vpn")
	// means the vpn container holds the tunnel. Use its HTTP API and don't
	// show the per-OS openvpn install hint.
	if os.Getenv("VPNGATE_HOST") != "" && cfg.VPNGateHost != "127.0.0.1" {
		ctrl := vpngate.NewController(cfg.VPNGateHost, cfg.VPNGateCtrlPort, time.Duration(cfg.VPNGateRotateInterval)*time.Second)
		vpnProvider = &sidecarAdapter{ctrl: ctrl}
	} else {
		var err error
		vpnProvider, err = vpn.NewProvider(vpn.ProviderConfig{
			Enabled:    cfg.VPNEnabled,
			Provider:   cfg.VPNProvider,
			SocksAddr:  cfg.SOCKSAddr,
			Country:    "", // TODO: wire VPNGATE_COUNTRY filter if set
			MinScore:   0,
			MaxPing:    0,
			RefreshInt: time.Duration(cfg.VPNGateRotateInterval) * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("create vpn provider: %w", err)
		}
	}

	// One shared dialer routes upstreams; direct vs tunnel is switched
	// live from the dashboard (replaces the old static BYPASS_PROXY env).
	dialer := upstream.NewDialer(cfg.SOCKSAddr)
	// If embedded VPN fell back to direct at construction (openvpn missing),
	// ensure dialer is direct so upstreams don't try SOCKS with no listener.
	// Sidecar mode already has SOCKS via vpn:9050, so skip this.
	if _, isSidecar := vpnProvider.(*sidecarAdapter); !isSidecar && vpnProvider.CurrentIP() == "direct" {
		dialer.SetDirect(true)
	}

	opencode := upstream.NewOpenCodeUpstream(
		cfg.UpstreamURLOpenCode,
		cfg.UpstreamKeyOpenCode,
		dialer,
		cfg.UpstreamOpenCodeFreeAllowlist,
	)
	kilo := upstream.NewKiloUpstream(
		cfg.UpstreamURLKilo,
		cfg.UpstreamKeyKilo,
		dialer,
	)
	llm7 := upstream.NewLLM7Upstream(cfg.UpstreamURLLLM7, dialer)

	infraRouter := upstream.NewRouter(opencode, kilo, llm7)
	appRouter := &routerAdapter{Router: infraRouter}

	m := metrics.New()

	cs := application.NewChatService(appRouter, m)
	ms := application.NewModelService(infraRouter)

	rec := recorder.NewRecorder(m.Snapshot)
	rec.SetModelsFunc(ms.AllModels)
	rec.SetVPNIPFunc(func() string {
		if dialer.IsDirect() {
			return "direct"
		}
		return vpnProvider.CurrentIP()
	})
	cs.WithRequestLogger(rec.RecordRequestLog)

	tpl, err := ui.LoadTemplates(web.Templates())
	if err != nil {
		return nil, fmt.Errorf("load UI templates: %w", err)
	}

	uiHandler := ui.NewHandler(rec, &vpnUI{provider: vpnProvider, dialer: dialer}, tpl, web.Static())
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
		cfg:         cfg,
		httpSrv:     httpSrv,
		logger:      logger,
		vpnProvider: vpnProvider,
		opencode:    opencode,
		kilo:        kilo,
		llm7:        llm7,
		rec:         rec,
		rateLimit:   rl,
	}, nil
}

// Run starts background workers (upstream refreshers, VPN IP monitor,
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
		s.llm7.Start(bgCtx, time.Duration(s.cfg.UpstreamRefreshLLM7)*time.Second)
	}()
	go func() {
		defer s.wg.Done()
		s.rec.Start(bgCtx)
	}()

	// VPN provider in-process (replaces external supervisor sidecar)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.vpnProvider.Start(bgCtx); err != nil {
			s.logger.Error("vpn provider failed", "error", err)
		}
	}()

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
			s.wg.Wait()
			_ = s.vpnProvider.Close()
			s.rateLimit.Stop()
			return fmt.Errorf("server failed: %w", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	// Signal background workers to stop
	cancelBG()

	// Wait for all background workers to finish
	// But first wait for HTTP server to shut down
	if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
		s.logger.Error("server forced to shutdown", "error", err)
		_ = s.vpnProvider.Close()
		s.rateLimit.Stop()
		return err
	}

	// Wait for background workers to complete
	s.wg.Wait()

	_ = s.vpnProvider.Close()
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
