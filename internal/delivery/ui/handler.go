package ui

import (
	"html/template"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"freegate/internal/infrastructure/vpngate"
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
	VPNIP() string
}

// VPNClient is the subset of the VPN controller the dashboard needs to
// render the server picker, drive manual connects, and check connectivity.
type VPNClient interface {
	ListServers() ([]vpngate.ServerInfo, error)
	ConnectTo(hostname string) error
	ForceNewIP() error
	Status() (vpngate.StatusInfo, error)
	Ping() (vpngate.PingResult, error)
	SetDirect(direct bool) error
	Direct() bool
	CurrentIP() string
}

// Handler serves the dashboard UI.
type Handler struct {
	data      DataSource
	vpn       VPNClient
	templates *template.Template
	staticFS  fs.FS
}

// NewHandler creates a Handler with the given data source, VPN client,
// parsed templates, and static FS.
func NewHandler(data DataSource, vpn VPNClient, tpl *template.Template, staticFS fs.FS) *Handler {
	return &Handler{
		data:      data,
		vpn:       vpn,
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
	r.Get("/partials/playground/models", h.partialPlaygroundModels)

	r.Get("/api/timeseries", h.apiTimeseries)
	r.Get("/api/health", h.apiHealth)

	// VPN server picker (manual connect, no automatic 429 handling)
	r.Get("/api/vpn/servers", h.apiVPNServers)
	r.Get("/api/vpn/status", h.apiVPNStatus)
	r.Post("/api/vpn/connect", h.apiVPNConnect)
	r.Post("/api/vpn/rotate", h.apiVPNRotate)
	r.Post("/api/vpn/ping", h.apiVPNPing)
	r.Post("/api/vpn/direct", h.apiVPNDirect)

	r.Get("/static/*", func(w http.ResponseWriter, req *http.Request) {
		req.URL.Path = "/" + chi.URLParam(req, "*")
		http.FileServer(http.FS(h.staticFS)).ServeHTTP(w, req)
	})

	return r
}
