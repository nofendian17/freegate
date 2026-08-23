// Command vpngate-supervisor runs inside the freegate "vpn" sidecar
// container. It wraps the shared tunnel core
// (internal/infrastructure/vpn/supervisor) — the same core the single-
// binary build uses in-process — with env-based configuration and a small
// HTTP control API that the freegate proxy uses to rotate the exit IP.
//
// Environment:
//
//	VPNGATE_SOCKS_PORT      SOCKS5 listen port (default 9050)
//	VPNGATE_CTRL_PORT       control API listen port (default 8080)
//	VPNGATE_COUNTRY         optional country filter (name or ISO code;
//	                        prefix with ! to exclude, e.g. !Japan)
//	VPNGATE_MIN_SCORE       optional minimum server score
//	VPNGATE_MAX_PING        optional maximum ping (ms)
//	VPNGATE_REFRESH_SECONDS how often to refresh the server list (default 300)
//	VPNGATE_LOG_LEVEL       slog level (default info)
//
// Security note: VPNGate servers are community-run and NOT trusted. Upstream
// calls are HTTPS end-to-end so API keys stay protected, but the relay sees
// destination metadata. This provides IP rotation, not anonymity.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"freegate/internal/infrastructure/vpn/supervisor"
)

func main() {
	if os.Getenv("VPNGATE_LOG_LEVEL") == "debug" {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}
	slog.Info("vpngate: supervisor starting", "debug", os.Getenv("VPNGATE_LOG_LEVEL"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctrlAddr := "0.0.0.0:" + envStr("VPNGATE_CTRL_PORT", "8080")
	s := supervisor.New(supervisor.Config{
		SocksAddr:  "0.0.0.0:" + envStr("VPNGATE_SOCKS_PORT", "9050"),
		Country:    envStr("VPNGATE_COUNTRY", ""),
		MinScore:   envInt("VPNGATE_MIN_SCORE", 0),
		MaxPing:    envInt("VPNGATE_MAX_PING", 0),
		RefreshInt: time.Duration(envInt("VPNGATE_REFRESH_SECONDS", 300)) * time.Second,
	})

	if err := s.Start(ctx); err != nil {
		slog.Error("vpngate: failed to start tunnel supervisor", "error", err)
	}

	httpSrv := &http.Server{Addr: ctrlAddr, Handler: routes(ctrlAddr, s)}
	go func() {
		slog.Info("vpngate: control API listening", "addr", ctrlAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("vpngate: control API exited", "error", err)
		}
	}()

	// Block until told to stop, then shut down gracefully.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	<-ch
	slog.Info("vpngate: shutting down")

	// 1. Signal goroutines to stop (they check the core's ctx).
	cancel()

	// 2. Stop accepting new HTTP requests and drain in-flight ones.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("vpngate: HTTP server forced to shutdown", "error", err)
	}

	// 3. Stop background loops and the openvpn process.
	if err := s.Close(); err != nil {
		slog.Warn("vpngate: supervisor close", "error", err)
	}
}

// routes wires the control API used by the freegate proxy onto the
// shared supervisor core.
func routes(ctrlAddr string, s *supervisor.Supervisor) http.Handler {
	mux := http.NewServeMux()

	// POST /ping runs a live connectivity check through the tunnel: DNS
	// resolution, an HTTPS egress probe (with latency), and the current
	// tunnel state. Used by the dashboard's "ping" button.
	mux.HandleFunc("POST /ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Ping())
	})

	mux.HandleFunc("POST /rotate", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Rotate(); err != nil {
			if errors.Is(err, supervisor.ErrRotationInProgress) {
				writeErr(w, http.StatusConflict, err)
				return
			}
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ip": s.Status().IP})
	})

	// GET /servers lists the currently available servers (after filters)
	// so the dashboard can render a picker.
	mux.HandleFunc("GET /servers", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.ListServers()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"servers": out})
	})

	// POST /servers/refresh forces a live re-fetch of the vpngate list and
	// swaps the cache, so the dashboard can pick up freshly online relays
	// instead of waiting for the refresh interval to elapse.
	mux.HandleFunc("POST /servers/refresh", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.RefreshServers()
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"servers": out})
	})

	// POST /connect connects the tunnel to a specific server chosen by the
	// user: {"server": "<hostname>"}. Returns 404 if the hostname is not
	// in the current list.
	mux.HandleFunc("POST /connect", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Server string `json:"server"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Server == "" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf(`body must be {"server": "<hostname>"}`))
			return
		}
		if err := s.ConnectTo(req.Server); err != nil {
			switch {
			case errors.Is(err, supervisor.ErrRotationInProgress):
				writeErr(w, http.StatusConflict, err)
			case errors.Is(err, supervisor.ErrServerNotFound):
				writeErr(w, http.StatusNotFound, err)
			default:
				writeErr(w, http.StatusInternalServerError, err)
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ip": s.Status().IP})
	})

	mux.HandleFunc("GET /ip", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ip": s.CurrentIP()})
	})

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Status())
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if !s.Healthy() {
			http.Error(w, "not connected", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	slog.Error("vpngate: request failed", "error", err)
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
