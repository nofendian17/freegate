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
// Mount it at a prefix (e.g. r.Mount("/ui", h.Routes())) and the subrouter
// handles paths like "/", "/partials/stats", "/static/*", etc.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Get("/", h.dashboard)
	r.Get("/index.html", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusMovedPermanently)
	})

	r.Get("/partials/stats", h.partialStats)
	r.Get("/partials/requests", h.partialRequests)
	r.Get("/partials/models", h.partialModels)

	r.Get("/api/timeseries", h.apiTimeseries)
	r.Get("/api/health", h.apiHealth)

	r.Get("/static/*", func(w http.ResponseWriter, req *http.Request) {
		req.URL.Path = "/" + chi.URLParam(req, "*")
		http.FileServer(http.FS(h.staticFS)).ServeHTTP(w, req)
	})

	return r
}
