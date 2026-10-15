package ui

import (
	"crypto/subtle"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"freegate/internal/delivery/middleware"
	"freegate/internal/domain"
	"freegate/internal/httputil"
)

// DataSource provides the data the UI needs to render.
type DataSource interface {
	Metrics() map[string]any
	Models() []domain.Model
	Requests() []domain.RequestLogEntry
	Timeseries() []domain.TimeseriesEntry
	UptimeSeconds() int64
	StartedAtUnix() int64
}

// Handler serves the dashboard UI.
type Handler struct {
	data       DataSource
	templates  *template.Template
	staticFS   fs.FS
	adminToken string
	// login throttles failed logins with an increasing wait (nil disables
	// throttling; tests build the Handler struct directly).
	login *middleware.AuthThrottle
}

// New creates a Handler with the given data source, parsed templates, and
// static FS. If adminToken is non-empty, Login/Logout and dashboard auth
// flows are enabled.
func New(data DataSource, tpl *template.Template, staticFS fs.FS, adminToken ...string) *Handler {
	var tok string
	if len(adminToken) > 0 {
		tok = adminToken[0]
	}
	return &Handler{
		data:       data,
		templates:  tpl,
		staticFS:   staticFS,
		adminToken: tok,
		login:      middleware.NewAuthThrottle(),
	}
}

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
	Title   string
	Error   string
	Next    string
	Scripts []string
}

// LoginPage renders the login form. Public, no auth.
func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "login.html", loginData{Title: "freegate — login", Next: next}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// loginWaitSeconds rounds a duration up to whole seconds for display and
// the Retry-After header.
func loginWaitSeconds(d time.Duration) int {
	s := int(d / time.Second)
	if d%time.Second != 0 {
		s++
	}
	return s
}

// Login validates admin_token from POST form and sets a signed session
// cookie on success. Failed attempts are throttled with an increasing
// per-IP wait (see middleware.AuthThrottle): a blocked attempt is rejected
// before the token is even compared, and each failure tells the caller how
// long to wait.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	ip := httputil.ClientIP(r)
	if wait := h.login.Blocked(ip); wait > 0 {
		secs := loginWaitSeconds(wait)
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = h.templates.ExecuteTemplate(w, "login.html", loginData{Title: "freegate — login", Error: fmt.Sprintf("too many failed attempts — try again in %ds", secs), Next: r.URL.Query().Get("next")})
		return
	}
	_ = r.ParseForm()
	token := r.FormValue("admin_token")
	next := r.FormValue("next")
	if next == "" {
		next = r.URL.Query().Get("next")
	}
	// Only same-origin paths are allowed: reject protocol-relative ("//x")
	// and backslash forms ("/\evil.example" — browsers normalize "\" to "/",
	// turning it into a protocol-relative hop).
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
		next = "/"
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(h.adminToken)) != 1 {
		msg := "invalid token"
		if wait := loginWaitSeconds(h.login.Fail(ip)); wait > 0 {
			msg = fmt.Sprintf("invalid token — try again in %ds", wait)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = h.templates.ExecuteTemplate(w, "login.html", loginData{Title: "freegate — login", Error: msg, Next: r.URL.Query().Get("next")})
		return
	}
	session, err := middleware.IssueAdminSession(h.adminToken)
	if err != nil {
		http.Error(w, "failed to issue session", http.StatusInternalServerError)
		return
	}
	h.login.Reset(ip)
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.AdminSessionCookie,
		Value:    session,
		Path:     "/",
		MaxAge:   int(middleware.AdminSessionTTL / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
	})
	http.Redirect(w, r, next, http.StatusFound)
}

// Logout clears the admin cookie and redirects to /login.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.AdminSessionCookie,
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
	r.Get("/providers", h.providersPage)
	r.Get("/settings", h.settingsPage)
	r.Get("/index.html", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusMovedPermanently)
	})

	r.Get("/partials/stats", h.partialStats)
	r.Get("/partials/upstreams", h.partialUpstreams)
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
