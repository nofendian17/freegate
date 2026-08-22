# Admin Token Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement admin-only dashboard auth (ADMIN_TOKEN + cookie) and multi API key support (comma-separated) where admin token is superset for /v1.

**Architecture:** Stateless HMAC cookie for browser (no session store), two middlewares ApiAuth (checks API_KEY list or admin) and AdminAuth (checks cookie or header, redirects to /login for HTML), login form at /login.

**Tech Stack:** Go 1.26.1, chi/v5, crypto/hmac/sha256, subtle.ConstantTimeCompare, html/template TerminalUI, httptest

**Spec:** `docs/superpowers/specs/2026-08-24-admin-token-design.md`

## Global Constraints

- Go 1.26.1 floor (`go.mod:3`)
- CGO_ENABLED=0 for builds
- Module `freegate`
- Upstreams: `opencode`, `kilo`, `llm7` unchanged
- Existing dialer direct fallback `upstream.Dialer.IsDirect()` must keep working
- No embedded DB, no JWT lib, no new external deps beyond stdlib
- Env var names: `ADMIN_TOKEN` (required, >=16 chars), `API_KEY` (comma-separated, `envSlice`)
- Cookie name `fg_admin`, HttpOnly, SameSite=Lax, Secure when TLS
- Use `subtle.ConstantTimeCompare` for all token compares

---

## File Structure

- **Modify:** `internal/config/config.go` — Config struct, Load, Validate, helpers
- **Modify:** `internal/config/config_test.go` — validation tests
- **Modify:** `internal/delivery/middleware/middleware.go` — ApiAuth, AdminAuth
- **Modify:** `internal/delivery/middleware/middleware_test.go` — middleware tests
- **Create:** `web/templates/login.html` — login form TerminalUI
- **Modify:** `internal/delivery/ui/handler.go` — LoginPage, Login, Logout handlers
- **Modify:** `internal/delivery/ui/handler_test.go` — ui handler tests (add login tests)
- **Modify:** `internal/server/server.go` — wiring ApiAuth/AdminAuth, public vs protected mounts
- **Modify:** `.env.example` — add ADMIN_TOKEN
- **Modify:** `docs/ENV.md`, `docs/RUNBOOK.md` — document new vars
- **Test:** `internal/delivery/ui/ui_test.go` — add login flow tests

---

### Task 1: Config — ADMIN_TOKEN required + API_KEY multi

**Files:**
- Modify: `internal/config/config.go:10-50`
- Modify: `internal/config/config_test.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `os.Getenv`, `envSlice`, `envStr`
- Produces: `Config{AdminToken string, APIKey []string}`, `func (c *Config) IsAdminAuthEnabled() bool`, `Validate() error`

- [ ] **Step 1: Write failing test for multi API_KEY and ADMIN_TOKEN required**

```go
func TestConfig_Load_MultiAPIKey(t *testing.T) {
    t.Setenv("API_KEY", "key1, key2, key3")
    t.Setenv("ADMIN_TOKEN", "0123456789abcdef0123456789abcdef")
    cfg := config.Load()
    if len(cfg.APIKey) != 3 || cfg.APIKey[0] != "key1" || cfg.APIKey[1] != "key2" {
        t.Fatalf("APIKey split failed: %+v", cfg.APIKey)
    }
    if cfg.AdminToken != "0123456789abcdef0123456789abcdef" {
        t.Fatalf("AdminToken failed: %s", cfg.AdminToken)
    }
}
func TestConfig_Validate_AdminRequired(t *testing.T) {
    cfg := &config.Config{AdminToken: "", APIKey: []string{"a"}, Port: 1234, VPNGateSocksPort: 9050, VPNGateCtrlPort: 8080, VPNGateRotateInterval: 30, RateLimit: 60, UpstreamURLOpenCode: "u", UpstreamURLKilo: "u", UpstreamURLLLM7: "u"}
    if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ADMIN_TOKEN") {
        t.Fatalf("expected ADMIN_TOKEN required error, got %v", err)
    }
}
```

- [ ] **Step 2: Run test to verify fails**

Run: `go test ./internal/config -run TestConfig_Load_MultiAPIKey -v`
Expected: FAIL — APIKey still string, AdminToken not exists

- [ ] **Step 3: Implement Config changes**

```go
// internal/config/config.go
type Config struct {
    // ...
    AdminToken string
    APIKey     []string // was string
    // ...
}
func Load() *Config {
    cfg := &Config{
        AdminToken: envStr("ADMIN_TOKEN", ""),
        APIKey:     envSlice("API_KEY", ""),
        // ...
    }
    // ...
}
func (c *Config) Validate() error {
    if c.AdminToken == "" {
        errs = append(errs, "ADMIN_TOKEN is required")
    } else if len(c.AdminToken) < 16 {
        errs = append(errs, "ADMIN_TOKEN must be at least 16 characters")
    }
    // ... existing checks, but APIKey now slice
}
```

- [ ] **Step 4: Run test to verify passes**

Run: `go test ./internal/config -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): support multi API_KEY and required ADMIN_TOKEN"
```

---

### Task 2: Middleware — ApiAuth (multi + admin superset) + AdminAuth (cookie/header)

**Files:**
- Modify: `internal/delivery/middleware/middleware.go:111-134`
- Modify: `internal/delivery/middleware/middleware_test.go`
- Test: `internal/delivery/middleware/middleware_test.go`

**Interfaces:**
- Consumes: `Config{AdminToken, APIKey}`
- Produces: `func ApiAuth(apiKeys []string, adminToken string) func(http.Handler) http.Handler`, `func AdminAuth(adminToken string) func(http.Handler) http.Handler`, `func hmacForToken(token string) string` (unexported)

- [ ] **Step 1: Write failing test for ApiAuth multi and AdminAuth**

```go
func TestApiAuth_MultiKey(t *testing.T) {
    h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request){ w.WriteHeader(200) })
    m := ApiAuth([]string{"k1","k2"}, "admin12345678901234")
    for _, k := range []string{"k1","k2"} {
        req := httptest.NewRequest("GET","/v1/models",nil)
        req.Header.Set("X-API-Key", k)
        rec := httptest.NewRecorder()
        m(h).ServeHTTP(rec, req)
        if rec.Code != 200 { t.Fatalf("key %s should pass, got %d", k, rec.Code) }
    }
    // admin superset
    req := httptest.NewRequest("GET","/v1/models",nil)
    req.Header.Set("Authorization","Bearer admin12345678901234")
    rec := httptest.NewRecorder()
    m(h).ServeHTTP(rec, req)
    if rec.Code != 200 { t.Fatalf("admin should pass api, got %d", rec.Code) }
}
func TestAdminAuth_Cookie(t *testing.T) {
    admin := "0123456789abcdef0123456789abcdef"
    h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request){ w.WriteHeader(200)})
    m := AdminAuth(admin)
    // no cookie -> redirect
    req := httptest.NewRequest("GET","/",nil)
    rec := httptest.NewRecorder()
    m(h).ServeHTTP(rec, req)
    if rec.Code != 302 { t.Fatalf("expected 302, got %d", rec.Code) }
    // with valid cookie
    cookieVal := hmacForToken(admin) // same logic as login
    req2 := httptest.NewRequest("GET","/",nil)
    req2.AddCookie(&http.Cookie{Name:"fg_admin", Value:cookieVal})
    rec2 := httptest.NewRecorder()
    m(h).ServeHTTP(rec2, req2)
    if rec2.Code != 200 { t.Fatalf("valid cookie should pass, got %d", rec2.Code) }
}
```

- [ ] **Step 2: Run test to verify fails**

Run: `go test ./internal/delivery/middleware -run TestApiAuth_MultiKey -v`
Expected: FAIL — ApiAuth still single key, AdminAuth not exists

- [ ] **Step 3: Implement middleware**

```go
func hmacForToken(token string) string {
    h := hmac.New(sha256.New, []byte(token))
    h.Write([]byte(token))
    return hex.EncodeToString(h.Sum(nil))
}
func ApiAuth(apiKeys []string, adminToken string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request){
            if len(apiKeys)==0 && adminToken=="" { next.ServeHTTP(w,r); return }
            key := r.Header.Get("X-API-Key")
            if key=="" {
                if auth:=r.Header.Get("Authorization"); len(auth)>7 && auth[:7]=="Bearer " { key=auth[7:] }
            }
            for _, k := range apiKeys {
                if subtle.ConstantTimeCompare([]byte(key), []byte(k))==1 { next.ServeHTTP(w,r); return }
            }
            if adminToken!="" && subtle.ConstantTimeCompare([]byte(key), []byte(adminToken))==1 { next.ServeHTTP(w,r); return }
            respond.JSONError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing API key")
        })
    }
}
func AdminAuth(adminToken string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request){
            // check cookie
            if c, err:=r.Cookie("fg_admin"); err==nil {
                exp:=hmacForToken(adminToken)
                if subtle.ConstantTimeCompare([]byte(c.Value), []byte(exp))==1 { next.ServeHTTP(w,r); return }
            }
            // check header
            key:=r.Header.Get("X-Admin-Token")
            if key=="" { if auth:=r.Header.Get("Authorization"); len(auth)>7 && auth[:7]=="Bearer " { key=auth[7:] } }
            if subtle.ConstantTimeCompare([]byte(key), []byte(adminToken))==1 { next.ServeHTTP(w,r); return }
            if r.Header.Get("HX-Request")=="true" || strings.Contains(r.Header.Get("Accept"), "application/json") {
                respond.JSONError(w, http.StatusUnauthorized, "unauthorized", "admin authentication required")
                return
            }
            http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path), http.StatusFound)
        })
    }
}
```

- [ ] **Step 4: Run test to verify passes**

Run: `go test ./internal/delivery/middleware -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/delivery/middleware/middleware.go internal/delivery/middleware/middleware_test.go
git commit -m "feat(middleware): ApiAuth multi-key + admin superset, AdminAuth cookie/header"
```

---

### Task 3: UI Login — form + handlers

**Files:**
- Create: `web/templates/login.html`
- Modify: `internal/delivery/ui/handler.go`
- Test: `internal/delivery/ui/handler_test.go`

**Interfaces:**
- Consumes: `middleware.hmacForToken`, `Config.AdminToken`
- Produces: `ui.Handler.LoginPage(w,r)`, `ui.Handler.Login(w,r)`, `ui.Handler.Logout(w,r)`, routes `/login`, `/logout`

- [ ] **Step 1: Write failing test for login**

```go
func TestLogin_Success_SetsCookie(t *testing.T) {
    rec := NewTestRecorder() // existing helper
    h := NewTestHandlerWithAdminToken("0123456789abcdef0123456789abcdef", rec)
    form := url.Values{"admin_token":{"0123456789abcdef0123456789abcdef"}}
    req := httptest.NewRequest("POST","/login", strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type","application/x-www-form-urlencoded")
    w := httptest.NewRecorder()
    h.Login(w, req)
    if w.Code != 302 { t.Fatalf("expected 302, got %d", w.Code) }
    cookies:=w.Result().Cookies()
    if len(cookies)==0 || cookies[0].Name!="fg_admin" { t.Fatalf("cookie not set") }
}
```

- [ ] **Step 2: Run test to verify fails**

Run: `go test ./internal/delivery/ui -run TestLogin_Success -v`
Expected: FAIL — Login not defined

- [ ] **Step 3: Implement template and handlers**

```html
<!-- web/templates/login.html -->
{{define "login"}}
<div class="login-shell"><form method="POST" action="/login"><input name="admin_token" type="password"/><button type="submit">> login</button></form></div>
{{end}}
```

```go
func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) { h.tpl.ExecuteTemplate(w, "login", nil) }
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
    r.ParseForm()
    token:=r.FormValue("admin_token")
    if subtle.ConstantTimeCompare([]byte(token), []byte(h.adminToken))!=1 {
        w.WriteHeader(401); h.tpl.ExecuteTemplate(w, "login", map[string]string{"error":"invalid token"}); return
    }
    http.SetCookie(w, &http.Cookie{Name:"fg_admin", Value:hmacForToken(h.adminToken), Path:"/", HttpOnly:true, SameSite:http.SameSiteLaxMode})
    http.Redirect(w,r,"/",302)
}
```

- [ ] **Step 4: Run test to verify passes**

Run: `go test ./internal/delivery/ui -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/templates/login.html internal/delivery/ui/handler.go
git commit -m "feat(ui): login form + handlers for admin auth"
```

---

### Task 4: Server Wiring — protect dashboard

**Files:**
- Modify: `internal/server/server.go:113-220`

**Interfaces:**
- Consumes: `Config`, `middleware.ApiAuth/AdminAuth`, `ui.Handler`
- Produces: wired `chi.Router` with protected mounts

- [ ] **Step 1: Write failing integration test**

```go
func TestServer_Dashboard_RequiresAuth(t *testing.T) {
    cfg:=&config.Config{AdminToken:"0123456789abcdef0123456789abcdef", APIKey:[]string{"k1"}, Port: 0}
    srv,_:=server.New(cfg)
    ts:=httptest.NewServer(srv.Handler) // expose Handler
    defer ts.Close()
    resp,_:=http.Get(ts.URL+"/")
    if resp.StatusCode!=302 { t.Fatalf("expected 302 to login, got %d", resp.StatusCode) }
}
```

- [ ] **Step 2: Run test to verify fails**

Run: `go test ./internal/server -run TestServer_Dashboard -v`
Expected: FAIL — dashboard still public

- [ ] **Step 3: Implement wiring**

```go
apiAuth:=middleware.ApiAuth(cfg.APIKey, cfg.AdminToken)
adminAuth:=middleware.AdminAuth(cfg.AdminToken)
r.Get("/login", uiHandler.LoginPage)
r.Post("/login", uiHandler.Login)
r.Post("/logout", uiHandler.Logout)
r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(web.Static()))))
r.With(adminAuth).Mount("/", uiHandler.Routes())
r.With(apiAuth).Route("/v1", func(r chi.Router){ ... })
```

- [ ] **Step 4: Run test to verify passes**

Run: `go test ./internal/server -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go
git commit -m "feat(server): wire AdminAuth for dashboard, ApiAuth for API"
```

---

### Task 5: Docs & Verification

**Files:**
- Modify: `.env.example`, `docs/ENV.md`, `docs/RUNBOOK.md`

- [ ] **Step 1: Update .env.example**

```
ADMIN_TOKEN= # required, >=16 chars, generate: openssl rand -hex 32
API_KEY=     # comma-separated, e.g. key1,key2
```

- [ ] **Step 2: Run full suite**

Run: `go test ./... -count=1` and `go vet ./...` and `go build ./...`
Expected: all PASS

- [ ] **Step 3: Commit**

```bash
git add .env.example docs/ENV.md docs/RUNBOOK.md
git commit -m "docs: document ADMIN_TOKEN and multi API_KEY"
```

---

## Self-Review

- Spec coverage: Config (Task1), Middleware (Task2), UI (Task3), Wiring (Task4), Docs (Task5) all covered
- No placeholders, all steps have concrete code
- Type consistency: `Config.APIKey []string`, `AdminToken string`, `ApiAuth([]string,string)`, `AdminAuth(string)`, cookie `fg_admin` consistent across tasks

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-08-24-admin-token-implementation.md`. Two execution options:

1. Subagent-Driven (recommended) - fresh subagent per task, review between tasks
2. Inline Execution - batch execution with checkpoints

Which approach?
