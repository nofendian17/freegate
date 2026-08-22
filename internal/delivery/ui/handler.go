package ui

import (
	"crypto/subtle"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"freegate/internal/delivery/middleware"
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
	RefreshServers() ([]vpngate.ServerInfo, error)
	ConnectTo(hostname string) error
	ForceNewIP() error
	Status() (vpngate.StatusInfo, error)
	Ping() (vpngate.PingResult, error)
	SetDirect(direct bool) error
	Direct() bool
	CurrentIP() string
	InstallHint() string
}

// Handler serves the dashboard UI.
type Handler struct {
	data       DataSource
	vpn        VPNClient
	templates  *template.Template
	staticFS   fs.FS
	adminToken string
}

// NewHandler creates a Handler with the given data source, VPN client,
// parsed templates, and static FS. If adminToken is non-empty, Login/Logout
// and dashboard auth flows are enabled.
func NewHandler(data DataSource, vpn VPNClient, tpl *template.Template, staticFS fs.FS, adminToken ...string) *Handler {
	var tok string
	if len(adminToken) > 0 {
		tok = adminToken[0]
	}
	return &Handler{
		data:       data,
		vpn:        vpn,
		templates:  tpl,
		staticFS:   staticFS,
		adminToken: tok,
	}
}

func hmacForToken(token string) string { return middleware.HmacForToken(token) }

func isSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	return false
}

type loginData struct {
	Error string
	Next  string
}

// LoginPage renders the login form. Public, no auth.
func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.templates.ExecuteTemplate(w, "login.html", loginData{Next: next})
}

// Login validates admin_token from POST form, sets HMAC cookie on success.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	token := r.FormValue("admin_token")
	next := r.FormValue("next")
	if next == "" {
		next = r.URL.Query().Get("next")
	}
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/"
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(h.adminToken)) != 1 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = h.templates.ExecuteTemplate(w, "login.html", loginData{Error: "invalid token", Next: r.URL.Query().Get("next")})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "fg_admin",
		Value:    hmacForToken(h.adminToken),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
	})
	http.Redirect(w, r, next, http.StatusFound)
}

// Logout clears the admin cookie and redirects to /login.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "fg_admin",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
	})
	http.Redirect(w, r, "/login", http.StatusFound)
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
	r.Post("/api/vpn/servers/refresh", h.apiVPNRefreshServers)
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
