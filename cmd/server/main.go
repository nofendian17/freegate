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

	"freegate/internal/config"
	"freegate/internal/handler"
	"freegate/internal/middleware"
	"freegate/internal/proxy"
	"freegate/internal/tor"
	"freegate/internal/collector"
	"freegate/internal/ui"
	"freegate/internal/upstream"
	"freegate/web"
)

const (
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 30 * time.Second
	serverIdleTimeout       = 120 * time.Second
	shutdownTimeout         = 10 * time.Second
	torMonitorInterval      = 5 * time.Minute
)

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

	router := upstream.NewRouter(opencode, kilo)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opencode.Start(ctx, time.Duration(cfg.UpstreamRefreshOpenCode)*time.Second)
	kilo.Start(ctx, time.Duration(cfg.UpstreamRefreshKilo)*time.Second)

	pc := proxy.NewClient(router).WithTorController(tc)

	// Wire the collector: receives one log entry per completed proxied request.
	recorder := collector.NewRecorder(pc.Metrics)
	recorder.SetModelsFunc(pc.AllModels)
	recorder.SetTorIPFunc(tc.CurrentIP)
	pc.WithRequestLogger(recorder.RecordRequestLog)
	recorder.Start(ctx)

	tpl, err := ui.LoadTemplates(web.Templates())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load UI templates: %v\n", err)
		os.Exit(1)
	}

	uiHandler := ui.NewHandler(recorder, tpl, web.Static())

	h := handler.New(pc)

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
