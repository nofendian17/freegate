package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"freegate/internal/httputil"
)

type statCard struct {
	Label string
	Value string
}

type statCardsView struct {
	Cards    []statCard
	Upstream []upstreamStat
}

type upstreamStat struct {
	Name  string
	Count int64
	Pct   int
}

func buildStatsData(m map[string]any) statCardsView {
	total := httputil.Int64(m["total_requests"])
	errors := httputil.Int64(m["upstream_errors"])
	inputTokens := httputil.Int64(m["input_tokens"])
	outputTokens := httputil.Int64(m["output_tokens"])

	perUp := map[string]int64{}
	if raw, ok := m["per_upstream"].(map[string]int64); ok {
		perUp = raw
	}

	cards := []statCard{
		{Label: "Total Requests", Value: fmt.Sprintf("%d", total)},
		{Label: "Upstream Errors", Value: fmt.Sprintf("%d", errors)},
		{Label: "Input Tokens", Value: fmt.Sprintf("%d", inputTokens)},
		{Label: "Output Tokens", Value: fmt.Sprintf("%d", outputTokens)},
	}

	var upstream []upstreamStat
	upTotal := int64(0)
	for _, n := range []string{"opencode", "kilo", "llm7"} {
		upTotal += perUp[n]
	}
	for _, n := range []string{"opencode", "kilo", "llm7"} {
		if perUp[n] == 0 && len(upstream) > 0 {
			continue // hide zero rows except the first
		}
		upstream = append(upstream, upstreamStat{
			Name:  n,
			Count: perUp[n],
			Pct:   pctOf(perUp[n], upTotal),
		})
	}

	return statCardsView{
		Cards:    cards,
		Upstream: upstream,
	}
}

func (h *Handler) partialStats(w http.ResponseWriter, r *http.Request) {
	data := buildStatsData(h.data.Metrics())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "partials/stats.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// partialUpstreams refreshes the upstream distribution card. Same view model
// as the stats partial; separate target so each card swaps independently.
func (h *Handler) partialUpstreams(w http.ResponseWriter, r *http.Request) {
	data := buildStatsData(h.data.Metrics())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "partials/upstreams.html", data.Upstream); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type requestRow struct {
	Time       string
	Model      string
	Upstream   string
	Status     string
	StatusTone string
	Duration   string
	Tokens     string
	IP         string
	Error      string
	HasError   bool
	ErrIdx     int
	FullError  string
}

type requestRowsView []requestRow

func (h *Handler) buildRequestRows() requestRowsView {
	entries := h.data.Requests()
	rows := make(requestRowsView, 0, len(entries))

	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		model := e.Model
		if model == "" {
			model = "—"
		}
		upstream := e.Upstream
		if upstream == "" {
			upstream = "—"
		}
		errStr := e.Error
		if len(errStr) > 60 {
			errStr = errStr[:60] + "…"
		}
		rows = append(rows, requestRow{
			Time:       e.Ts.UTC().Format("15:04:05"),
			Model:      model,
			Upstream:   upstream,
			Status:     fmt.Sprintf("%d", e.Status),
			StatusTone: toneForStatus(e.Status),
			Duration:   fmt.Sprintf("%dms", e.DurationMs),
			Tokens:     tokenDisplay(e.TotalTokens, e.PromptTokens, e.CompletionTokens),
			IP:         e.IP,
			Error:      errStr,
			HasError:   len(e.Error) > 0,
			ErrIdx:     len(rows),
			FullError:  e.Error,
		})
	}
	return rows
}

func (h *Handler) partialRequests(w http.ResponseWriter, r *http.Request) {
	rows := h.buildRequestRows()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "partials/requests.html", rows); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type modelRow struct {
	ID       string
	Provider string
	IsFree   bool
}

type modelRowsView []modelRow

func (h *Handler) buildModelRows(provider string) modelRowsView {
	provider = strings.ToLower(strings.TrimSpace(provider))

	models := h.data.Models()
	rows := make(modelRowsView, 0, len(models))

	for _, m := range models {
		if provider != "" && provider != "all" && strings.ToLower(m.Provider) != provider {
			continue
		}
		rows = append(rows, modelRow{
			ID:       m.ID,
			Provider: m.Provider,
			IsFree:   m.IsFree,
		})
	}
	return rows
}

func (h *Handler) partialModels(w http.ResponseWriter, r *http.Request) {
	provider := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	rows := h.buildModelRows(provider)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "partials/models.html", rows); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// toneForStatus maps an HTTP status to a semantic tone name. The name is
// emitted as a data-status-tone test hook; visual styling comes from the
// template's {{if eq}} branches (achromatic palette + ember, per design.md).
func toneForStatus(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "green"
	case code >= 300 && code < 400:
		return "blue"
	case code == 429:
		return "amber"
	case code >= 400 && code < 500:
		return "amber"
	case code >= 500:
		return "red"
	default:
		return "gray"
	}
}

func tokenDisplay(total, prompt, completion int) string {
	if total == 0 {
		return "—"
	}
	if prompt > 0 || completion > 0 {
		return fmt.Sprintf("%d (%d↑ %d↓)", total, prompt, completion)
	}
	return fmt.Sprintf("%d", total)
}

func (h *Handler) apiTimeseries(w http.ResponseWriter, r *http.Request) {
	entries := h.data.Timeseries()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(entries); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type healthResp struct {
	OK         bool   `json:"ok"`
	Uptime     string `json:"uptime"`
	StartedAt  string `json:"started_at"`
	HasModels  bool   `json:"has_models"`
	ModelCount int    `json:"model_count"`
}

func (h *Handler) apiHealth(w http.ResponseWriter, r *http.Request) {
	models := h.data.Models()
	resp := healthResp{
		OK:         len(models) > 0,
		Uptime:     formatDuration(time.Duration(h.data.UptimeSeconds()) * time.Second),
		StartedAt:  fmt.Sprintf("%d", h.data.StartedAtUnix()),
		HasModels:  len(models) > 0,
		ModelCount: len(models),
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
