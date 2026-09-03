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
	Handler     http.Handler
	logger      *slog.Logger
	vpnProvider vpn.Provider
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

// vpnUI adapts the VPN provider for the dashboard: it wraps the Provider
// (ListServers/ConnectTo/ForceNewIP/Status/Ping/CurrentIP) and adds the live
// direct/tunnel switch backed by the shared upstream dialer.
type vpnUI struct {
	provider vpn.Provider
	dialer   *upstream.Dialer
}

func (v *vpnUI) ListServers() ([]vpn.ServerInfo, error)    { return v.provider.ListServers() }
func (v *vpnUI) RefreshServers() ([]vpn.ServerInfo, error) { return v.provider.RefreshServers() }
func (v *vpnUI) ConnectTo(hostname string) error           { return v.provider.ConnectTo(hostname) }
func (v *vpnUI) ForceNewIP() error                         { return v.provider.Rotate() }
func (v *vpnUI) Status() (vpn.StatusInfo, error)           { return v.provider.Status() }
func (v *vpnUI) Ping() (vpn.PingResult, error)             { return v.provider.Ping() }
func (v *vpnUI) CurrentIP() string                         { return v.provider.CurrentIP() }
func (v *vpnUI) InstallHint() string                       { return v.provider.InstallHint() }

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

func customUpstreams(mgr *upstream.ProviderManager) []domain.Upstream {
	all := mgr.All()
	out := make([]domain.Upstream, 0, len(all))
	for _, u := range all {
		out = append(out, u)
	}
	return out
}

func resolveActiveChain(pstore *providers.Store, lookup func(string) domain.Upstream) []domain.Upstream {
	active, err := pstore.ActiveCombo()
	if err != nil {
		return nil
	}
	var chain []domain.Upstream
	for _, m := range active.Members {
		if u := lookup(m); u != nil {
			chain = append(chain, u)
		}
	}
	return chain
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

	var vpnProvider vpn.Provider
	// Docker sidecar mode: VPNGATE_HOST explicitly set to non-loopback (e.g. "vpn")
	// means the vpn container holds the tunnel. Use its HTTP API and don't
	// show the per-OS openvpn install hint.
	if cfg.IsSidecarMode() {
		ctrl := vpngate.NewController(cfg.VPNGateHost, cfg.VPNGateCtrlPort, time.Duration(cfg.VPNGateRotateInterval)*time.Second)
		vpnProvider = &sidecarAdapter{ctrl: ctrl}
	} else {
		var err error
		vpnProvider, err = vpn.NewProvider(vpn.ProviderConfig{
			Enabled:    cfg.VPNEnabled,
			Provider:   cfg.VPNProvider,
			SocksAddr:  cfg.SOCKSAddr,
			Country:    cfg.VPNGateCountry,
			MinScore:   0,
			MaxPing:    0,
			RefreshInt: time.Duration(cfg.VPNGateRotateInterval) * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("create vpn provider: %w", err)
		}
	}

	// One shared dialer + transport routes all upstreams; direct vs tunnel
	// is switched live from the dashboard. Sharing the Transport pools idle
	// connections once instead of per-upstream.
	dialer := upstream.NewDialer(cfg.SOCKSAddr)
	// Single source for direct mode: Config.IsDirect().
	if cfg.IsDirect() {
		dialer.SetDirect(true)
	}
	// Safety fallback for openvpn-missing case where provider is direct
	// but cfg still carried SOCKSAddr.
	if _, isSidecar := vpnProvider.(*sidecarAdapter); !isSidecar {
		if vpnProvider.CurrentIP() == "direct" && !dialer.IsDirect() {
			dialer.SetDirect(true)
		}
	}
	sharedTr := buildSharedTransport(dialer)
	opencode, kilo, llm7, infraRouter := buildUpstreamsAndRouter(cfg, sharedTr)

	pstore, err := providers.Open(cfg.ProvidersDBPath)
	if err != nil {
		return nil, fmt.Errorf("open providers db: %w", err)
	}
	mgr := upstream.NewProviderManager(pstore, sharedTr)
	if err := mgr.Rebuild(); err != nil {
		logger.Warn("custom providers rebuild failed, keeping legacy", "error", err)
	}
	combo := upstream.NewComboRouter(infraRouter)
	combo.Register(opencode)
	combo.Register(kilo)
	combo.Register(llm7)
	for _, u := range mgr.All() {
		combo.Register(u)
	}
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
	combo.SetChain(resolveActiveChain(pstore, lookup))

	m := metrics.New()
	ms := application.NewModelService(combo)
	rec := recorder.NewRecorderWithDeps(recorder.Deps{
		Metrics: m.Snapshot,
		Models:  ms.AllModels,
		VPNIP: func() string {
			if dialer.IsDirect() {
				return "direct"
			}
			return vpnProvider.CurrentIP()
		},
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

	uiHandler := ui.NewHandler(rec, &vpnUI{provider: vpnProvider, dialer: dialer}, tpl, web.Static(), cfg.AdminToken)
	// Direct config for Responses models (e.g. muse-spark)
	handler.SetResponseModels(cfg.ResponseModels)
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

	// Dashboard (admin-only) — all uiHandler routes require AdminAuth.
	r.With(adminAuth).Mount("/", uiHandler.Routes())

	rebuild := func() error {
		if err := mgr.Rebuild(); err != nil {
			return err
		}
		combo.SyncRegistered(append([]domain.Upstream{opencode, kilo, llm7}, customUpstreams(mgr)...))
		combo.SetChain(resolveActiveChain(pstore, lookup))
		return nil
	}
	adminHandler := admin.New(pstore, rebuild, lookup, combo.SetChain)
	r.With(adminAuth).Mount("/", adminHandler.Routes())

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
		vpnProvider: vpnProvider,
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
	s.manager.Start(bgCtx)

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
			s.manager.Stop()
			_ = s.pstore.Close()
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
	s.manager.Stop()

	// Wait for all background workers to finish
	// But first wait for HTTP server to shut down
	if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
		s.logger.Error("server forced to shutdown", "error", err)
		_ = s.pstore.Close()
		_ = s.vpnProvider.Close()
		s.rateLimit.Stop()
		return err
	}

	// Wait for background workers to complete
	s.wg.Wait()

	_ = s.pstore.Close()
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
