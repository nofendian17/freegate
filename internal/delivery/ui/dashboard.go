package ui

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"time"
)

type endpoint struct {
	Method string
	Path   string
	Desc   string
}

type pageData struct {
	Title        string
	Active       string
	Meta         bool
	Playground   bool
	Palette      bool
	Scripts      []string
	Uptime       string
	StartedAt    string
	ModelCount   int
	RequestCount int
	Stats        template.HTML
	Requests     template.HTML
	Models       template.HTML
	Providers    []string
	Upstream     []upstreamStat
	Endpoints    []endpoint
}

// dashboard renders the main dashboard page with initial data inline.
// HTMX polling then keeps the dynamic sections (stats, upstreams, requests,
// models) fresh.
func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {

	m := h.data.Metrics()
	statsData := buildStatsData(m)

	uptime := time.Duration(h.data.UptimeSeconds()) * time.Second
	models := h.data.Models()
	seen := make(map[string]bool)
	var providers []string
	for _, model := range models {
		if model.Provider != "" && !seen[model.Provider] {
			seen[model.Provider] = true
			providers = append(providers, model.Provider)
		}
	}
	sort.Strings(providers)

	data := pageData{
		Providers:  providers,
		Title:      "freegate — dashboard",
		Active:     "dashboard",
		Meta:       true,
		Playground: true,
		Palette:    false,
		// chart.umd.js rides in Scripts (not a separate Chart flag) so all
		// page scripts stay ordered before alpine.min.js in layout/head.
		Scripts:      []string{"/static/js/chart.umd.js", "/static/js/dashboard.js", "/static/js/playground.js"},
		Uptime:       formatDuration(uptime),
		StartedAt:    time.Unix(h.data.StartedAtUnix(), 0).UTC().Format("2006-01-02 15:04:05 UTC"),
		ModelCount:   len(models),
		RequestCount: len(h.data.Requests()),
		Stats:        h.renderToString("partials/stats.html", statsData),
		Requests:     h.renderToString("partials/requests.html", h.buildRequestRows()),
		Models:       h.renderToString("partials/models.html", h.buildModelRows("")),
		Upstream:     statsData.Upstream,
		Endpoints: []endpoint{
			{Method: "GET", Path: "/v1/models", Desc: "list available free models"},
			{Method: "POST", Path: "/v1/chat/completions", Desc: "OpenAI-compatible chat completion"},
			{Method: "POST", Path: "/v1/messages", Desc: "Anthropic-compatible messages"},
			{Method: "POST", Path: "/v1/responses", Desc: "OpenAI Responses API"},
			{Method: "GET", Path: "/v1/metrics", Desc: "request metrics per upstream"},
			{Method: "GET", Path: "/ready", Desc: "health check"},
		},
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderToString(name string, data any) template.HTML {
	if data == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := h.templates.ExecuteTemplate(&buf, name, data); err != nil {
		return template.HTML(fmt.Sprintf("<!-- render error: %v -->", err))
	}
	return template.HTML(buf.String())
}

func pctOf(part, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(part * 100 / total)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}
