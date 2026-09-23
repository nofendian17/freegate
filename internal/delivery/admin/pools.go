package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"freegate/internal/delivery/respond"
	"freegate/internal/infrastructure/providers"
)

type poolIn struct {
	Name        string `json:"name" validate:"required,resource_name"`
	ProxyURL    string `json:"proxy_url" validate:"required,http_url,max=2048"`
	NoProxy     string `json:"no_proxy" validate:"omitempty,max=2048"`
	StrictProxy bool   `json:"strict_proxy"`
	Enabled     bool   `json:"enabled"`
}

func (h *Handler) listPools(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListPools(r.Context())
	if err != nil {
		respondStoreError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"data": rows})
}

func (h *Handler) createPool(w http.ResponseWriter, r *http.Request) {
	in := poolIn{Enabled: true}
	if !decodeInput(w, r, &in) {
		return
	}
	row, err := h.store.CreatePool(r.Context(), providers.ProxyPool{Name: in.Name, ProxyURL: in.ProxyURL, NoProxy: in.NoProxy, StrictProxy: in.StrictProxy, Enabled: in.Enabled})
	if err != nil {
		respondStoreError(w, err)
		return
	}
	if err := h.rebuild(r.Context()); err != nil {
		respondRebuildError(w, err)
		return
	}
	respond.JSON(w, http.StatusCreated, row)
}

func (h *Handler) getPool(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	row, err := h.store.GetPool(r.Context(), uint(id))
	if err != nil {
		respondStoreError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, row)
}

func (h *Handler) updatePool(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var in poolIn
	if !decodeInput(w, r, &in) {
		return
	}
	row, err := h.store.UpdatePool(r.Context(), uint(id), providers.ProxyPool{Name: in.Name, ProxyURL: in.ProxyURL, NoProxy: in.NoProxy, StrictProxy: in.StrictProxy, Enabled: in.Enabled})
	if err != nil {
		respondStoreError(w, err)
		return
	}
	if err := h.rebuild(r.Context()); err != nil {
		respondRebuildError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, row)
}

func (h *Handler) deletePool(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	// DeletePool also resets every pin on the pool in the same
	// transaction, so no provider is left pointing at a gone pool.
	if err := h.store.DeletePool(r.Context(), uint(id)); err != nil {
		respondStoreError(w, err)
		return
	}
	if err := h.rebuild(r.Context()); err != nil {
		respondRebuildError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testPool probes the relay with the same headers production traffic uses
// and persists the outcome. A failed probe disables the pool so the
// runtime stops routing to a dead relay.
func (h *Handler) testPool(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	row, err := h.store.GetPool(r.Context(), uint(id))
	if err != nil {
		respondStoreError(w, err)
		return
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), "GET", row.ProxyURL, nil)
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.Header.Set("x-relay-target", "https://httpbin.org")
	req.Header.Set("x-relay-path", "/get")
	client := &http.Client{Timeout: 10 * time.Second}
	if h.transport != nil {
		client.Transport = h.transport
	}
	resp, err := client.Do(req)
	elapsed := time.Since(start).Milliseconds()
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err != nil {
		h.persistPoolFailure(persistCtx, row, err.Error())
		respond.JSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "elapsedMs": elapsed})
		return
	}
	defer resp.Body.Close()
	ok := resp.StatusCode < 300
	lastErr := ""
	if !ok {
		lastErr = fmt.Sprintf("relay test failed with status %d", resp.StatusCode)
		h.persistPoolFailure(persistCtx, row, lastErr)
		respond.JSON(w, http.StatusOK, map[string]any{"ok": ok, "status": resp.StatusCode, "error": lastErr, "elapsedMs": elapsed})
		return
	}
	if err := h.store.MarkPoolTest(persistCtx, row.ID, true, ""); err != nil {
		slog.Warn("failed to persist successful pool test", "pool_id", row.ID, "error", err)
	}
	respond.JSON(w, http.StatusOK, map[string]any{"ok": ok, "status": resp.StatusCode, "error": lastErr, "elapsedMs": elapsed})
}

func (h *Handler) persistPoolFailure(ctx context.Context, row providers.ProxyPool, lastErr string) {
	if _, err := h.store.UpdatePool(ctx, row.ID, providers.ProxyPool{Name: row.Name, ProxyURL: row.ProxyURL, NoProxy: row.NoProxy, StrictProxy: row.StrictProxy, Enabled: false}); err != nil {
		slog.Warn("failed to disable failed pool", "pool_id", row.ID, "error", err)
	}
	if err := h.store.MarkPoolTest(ctx, row.ID, false, lastErr); err != nil {
		slog.Warn("failed to persist pool test", "pool_id", row.ID, "error", err)
	}
	if err := h.rebuild(ctx); err != nil {
		slog.Warn("failed to rebuild after pool test", "pool_id", row.ID, "error", err)
	}
}
