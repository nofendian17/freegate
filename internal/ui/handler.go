package ui

import (
	"html/template"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"freegate/internal/model"
)

// DataSource provides the data the UI needs to render.
type DataSource interface {
	Metrics() map[string]any
	Models() []model.Model
	Requests() []model.RequestLogEntry
	Timeseries() []model.TimeseriesEntry
	UptimeSeconds() int64
	StartedAtUnix() int64
	TorIP() string
}

// Handler serves the dashboard UI.
type Handler struct {
	data      DataSource
	templates *template.Template
	staticFS  fs.FS
}

// NewHandler creates a Handler with the given data source, parsed templates, and static FS.
func NewHandler(data DataSource, tpl *template.Template, staticFS fs.FS) *Handler {
	return &Handler{
		data:      data,
		templates: tpl,
		staticFS:  staticFS,
	}
}

// Routes returns a chi.Router with all UI routes relative to its mount point.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return r
}

// RegisterRoutes registers UI handler routes onto an existing router.
// Use this when mounting at root alongside other routes to avoid
// consuming unmatched paths with a blanket Mount.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.dashboard)
	r.Get("/index.html", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/", http.StatusMovedPermanently)
	})

	r.Get("/partials/stats", h.partialStats)
	r.Get("/partials/requests", h.partialRequests)
	r.Get("/partials/models", h.partialModels)

	r.Get("/api/timeseries", h.apiTimeseries)
	r.Get("/api/health", h.apiHealth)

	r.Get("/static/*", h.serveStatic)
}

func (h *Handler) serveStatic(w http.ResponseWriter, req *http.Request) {
	req.URL.Path = "/" + chi.URLParam(req, "*")
	http.FileServer(http.FS(h.staticFS)).ServeHTTP(w, req)
}
