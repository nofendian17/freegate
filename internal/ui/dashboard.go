package ui

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"time"
)

type pageData struct {
	Title         string
	Uptime        string
	StartedAt     string
	ModelCount    int
	RequestCount  int
	Stats         template.HTML
	Requests      template.HTML
	Models        template.HTML
	OpenCodeCount int64
	KiloCount     int64
	UpstreamPct   upstreamPercents
}

type upstreamPercents struct {
	OpenCode int
	Kilo     int
}

// dashboard renders the main dashboard page with initial data inline.
// HTMX polling then keeps the 3 dynamic sections (stats, requests, models) fresh.
func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {

	m := h.data.Metrics()
	perUp := map[string]int64{}
	if raw, ok := m["per_upstream"].(map[string]int64); ok {
		perUp = raw
	}
	opencode := perUp["opencode"]
	kilo := perUp["kilo"]
	total := opencode + kilo
	pct := upstreamPercents{OpenCode: pctOf(opencode, total), Kilo: pctOf(kilo, total)}

	uptime := time.Duration(h.data.UptimeSeconds()) * time.Second
	models := h.data.Models()

	data := pageData{
		Title:         "freegate dashboard",
		Uptime:        formatDuration(uptime),
		StartedAt:     time.Unix(h.data.StartedAtUnix(), 0).Format("2006-01-02 15:04:05 MST"),
		ModelCount:    len(models),
		RequestCount:  len(h.data.Requests()),
		Stats:         h.renderToString("partials/stats.html", buildStatsData(m)),
		Requests:      h.renderToString("partials/requests.html", h.buildRequestRows()),
		Models:        h.renderToString("partials/models.html", h.buildModelRows("")),
		OpenCodeCount: opencode,
		KiloCount:     kilo,
		UpstreamPct:   pct,
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
