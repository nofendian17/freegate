package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type statCard struct {
	Label string
	Value string
	Tone  string
}

type statCardsView struct {
	Cards    []statCard
	OpenCode int64
	Kilo     int64
}

func buildStatsData(m map[string]any) statCardsView {
	total := asInt64(m["total_requests"])
	retries := asInt64(m["retry_count"])
	errors := asInt64(m["upstream_errors"])
	rlHits := asInt64(m["rate_limit_hits"])

	perUp := map[string]int64{}
	if raw, ok := m["per_upstream"].(map[string]int64); ok {
		perUp = raw
	}

	cards := []statCard{
		{Label: "Total Requests", Value: fmt.Sprintf("%d", total), Tone: "blue"},
		{Label: "Retries", Value: fmt.Sprintf("%d", retries), Tone: "amber"},
		{Label: "Upstream Errors", Value: fmt.Sprintf("%d", errors), Tone: "red"},
		{Label: "Rate-Limit Hits", Value: fmt.Sprintf("%d", rlHits), Tone: "purple"},
	}

	return statCardsView{
		Cards:    cards,
		OpenCode: perUp["opencode"],
		Kilo:     perUp["kilo"],
	}
}

func (h *Handler) partialStats(w http.ResponseWriter, r *http.Request) {
	data := buildStatsData(h.data.Metrics())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "partials/stats.html", data); err != nil {
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
	IP         string
	Error      string
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
			Time:       e.Ts.Format("15:04:05"),
			Model:      model,
			Upstream:   upstream,
			Status:     fmt.Sprintf("%d", e.Status),
			StatusTone: toneForStatus(e.Status),
			Duration:   fmt.Sprintf("%dms", e.DurationMs),
			IP:         e.IP,
			Error:      errStr,
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
	ID           string
	Provider     string
	ProviderTone string
	IsFree       bool
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
			ID:           m.ID,
			Provider:     m.Provider,
			ProviderTone: toneForProvider(m.Provider),
			IsFree:       m.IsFree,
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

func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

func toneForProvider(p string) string {
	switch strings.ToLower(p) {
	case "opencode":
		return "blue"
	case "kilo":
		return "purple"
	default:
		return "gray"
	}
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
