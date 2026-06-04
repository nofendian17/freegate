package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"freegate/internal/application"
	"freegate/internal/config"
	"freegate/internal/delivery/handler"
	"freegate/internal/delivery/middleware"
	"freegate/internal/domain"
	"freegate/internal/infrastructure/metrics"
	"freegate/internal/infrastructure/recorder"
	"freegate/internal/infrastructure/tor"
	"freegate/internal/infrastructure/upstream"
	"freegate/internal/delivery/ui"
	"freegate/web"
)

const (
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 30 * time.Second
	serverIdleTimeout       = 120 * time.Second
	shutdownTimeout         = 10 * time.Second
	torMonitorInterval      = 5 * time.Minute
)

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

func main() {
	cfg := config.Load()

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid config:\n%s\n", err)
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	})))

	tc := tor.NewController(cfg.TorHost, cfg.CtrlPort, cfg.CtrlPass, cfg.SOCKSAddr)

	opencode := upstream.NewOpenCodeUpstream(
		cfg.UpstreamURLOpenCode,
		cfg.UpstreamKeyOpenCode,
		cfg.SOCKSAddr,
	)
	kilo := upstream.NewKiloUpstream(
		cfg.UpstreamURLKilo,
		cfg.UpstreamKeyKilo,
		cfg.SOCKSAddr,
		cfg.UpstreamKiloPrefixes,
	)

	infraRouter := upstream.NewRouter(opencode, kilo)
	appRouter := &routerAdapter{Router: infraRouter}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opencode.Start(ctx, time.Duration(cfg.UpstreamRefreshOpenCode)*time.Second)
	kilo.Start(ctx, time.Duration(cfg.UpstreamRefreshKilo)*time.Second)

	m := metrics.New()

	cs := application.NewChatService(appRouter, tc, m, 2, 3*time.Second)
	ms := application.NewModelService(infraRouter)

	// Wire the recorder: receives one log entry per completed proxied request.
	rec := recorder.NewRecorder(m.Snapshot)
	rec.SetModelsFunc(ms.AllModels)
	rec.SetTorIPFunc(tc.CurrentIP)
	cs.WithRequestLogger(rec.RecordRequestLog)
	rec.Start(ctx)

	tpl, err := ui.LoadTemplates(web.Templates())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load UI templates: %v\n", err)
		os.Exit(1)
	}

	uiHandler := ui.NewHandler(rec, tpl, web.Static())

	h := handler.New(cs, ms, m)

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
		r.Get("/models", h.ListModels)
		r.Get("/metrics", h.Metrics)
		r.Post("/chat/completions", h.Chat)
	})
	r.With(rl.Middleware, middleware.Auth(cfg.APIKey)).Post("/v1/messages", h.Chat)
	r.With(rl.Middleware, middleware.Auth(cfg.APIKey)).Get("/ready", h.Ready)

	stopIP := make(chan struct{})
	go tc.StartMonitor(torMonitorInterval, stopIP)

	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      0,
		IdleTimeout:       serverIdleTimeout,
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("starting server", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-sigCtx.Done()
	close(stopIP)
	slog.Info("shutting down server...")

	cancel()

	tc.Close()
	rl.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}

	slog.Info("server stopped gracefully")
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
