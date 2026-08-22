# Admin Token & Dashboard Auth Design — 2026-08-24

## Overview

Add admin-only protection to the dashboard and VPN switching, while allowing multiple
API keys (comma-separated) for `/v1/*`. A single `ADMIN_TOKEN` env (required) gates
all browser UI and admin APIs; any valid `API_KEY` entry **or** the admin token
grants access to the OpenAI-compatible API. No DB, no JWT — stateless token +
HttpOnly cookie session for HTMX.

## Goals / Non-Goals

**Goals:**
- Dashboard (`/`, `/partials/*`, `/api/*`) is admin-only (VPN switch, rotate, ping, etc.)
- `API_KEY` supports comma-separated list (`key1,key2`) — any entry valid
- `ADMIN_TOKEN` is required env, single high-entropy, also valid for `/v1/*` (superset)
- Login form for browsers (cookie), header auth for API/curl (`X-Admin-Token` / `Bearer`)
- Backward compatible for API clients (single key still works if they send old single value)

**Non-Goals:**
- User DB, roles/permissions beyond admin, JWT, OAuth, token rotation API, per-key rate limit

## Architecture

```
Browser → GET /login (public, TerminalUI form)
        → POST /login {admin_token} --subtle compare--> Set-Cookie fg_admin=HMAC(ADMIN_TOKEN) HttpOnly
        → 302 / → AdminAuth (cookie OR header) → dashboard

Browser/Admin API → /api/vpn/*, /partials/*, /api/health, /api/timeseries, /
                 → AdminAuth (cookie OR X-Admin-Token/Bearer) → handler
                 -- fail → 302 /login (Accept: text/html) or 401 JSON (HX-Request/JSON)

API Client → /v1/*, /v1/messages, /ready, /v1/metrics
           → ApiAuth(apiKeys, adminToken) --any match--> handler
           -- fail → 401 JSON

Public → /login, /logout, /static/*, /healthz (no auth)
```

**Key decision:** cookie value is not raw token but `HMAC-SHA256(ADMIN_TOKEN, ADMIN_TOKEN)` hex via `crypto/hmac` with `subtle.ConstantTimeCompare` on validation, so token is not stored plaintext in cookie jar. Alternative simpler: store raw token in HttpOnly cookie and compare directly — also ConstantTime; chosen HMAC to avoid leaking token via `document.cookie` even if `httpOnly` is bypassed via XSS (defense in depth). No server-side session store.

## Components

### 1. Config (`internal/config/config.go`)

- `AdminToken string` — `envStr("ADMIN_TOKEN","")` **required** (Validate rejects empty or <16 chars)
- `APIKey []string` — change from `string` to `[]string` via `envSlice("API_KEY","")` (trim, drop empty)
- Helpers:
  - `IsAdminAuthEnabled() bool { return c.AdminToken != "" }`
  - `IsDirect()` / `IsSidecarMode()` (already)
- `Validate()`:
  - `ADMIN_TOKEN is required`
  - `len(ADMIN_TOKEN) < 16` → error (entropy guard)
  - `API_KEY` entries each non-empty if provided
  - keep existing `IsDirect` SOCKSAddr logic
- `.env.example` add `ADMIN_TOKEN=<generate with openssl rand -hex 32>`

### 2. Middleware (`internal/delivery/middleware/middleware.go`)

- `ApiAuth(apiKeys []string, adminToken string) func(http.Handler) http.Handler`
  - Extract key via `X-API-Key` or `Authorization: Bearer <k>`
  - Check `subtle.ConstantTimeCompare` against each `apiKeys` entry; if none match, check `adminToken` (superset)
  - On fail: `respond.JSONError(401, "unauthorized", ...)`
- `AdminAuth(adminToken string) func(http.Handler) http.Handler`
  - Check 1: `Cookie fg_admin` → validate HMAC vs `adminToken`
  - Check 2: header `X-Admin-Token` or `Authorization: Bearer`
  - On fail:
    - If `r.Header.Get("HX-Request")=="true"` or `Accept: application/json` → 401 JSON
    - Else → `http.Redirect(w,r,"/login?next="+url.QueryEscape(r.URL.Path),302)`
- Keep existing `RateLimiter`, `RequestID`, `Logger`, `CORS`, `Recoverer`

### 3. UI Login (`internal/delivery/ui/handler.go`, `web/templates/login.html`, `web/templates/partials/login.html`)

- `GET /login` — render `login.html` (TerminalUI, extends `dashboard.html` style, form `POST /login` with `admin_token` input)
- `POST /login` — `r.ParseForm()`, `token:=r.FormValue("admin_token")`, `subtle.Compare` vs `cfg.AdminToken`, on success:
  - `hm:=hmac.New(sha256.New, []byte(cfg.AdminToken)); hm.Write([]byte(cfg.AdminToken)); cookieVal:=hex.EncodeToString(hm.Sum(nil))`
  - `http.SetCookie(w, &http.Cookie{Name:"fg_admin", Value:cookieVal, Path:"/", HttpOnly:true, SameSite:http.SameSiteLaxMode, Secure:isTLS(r)})`
  - Redirect to `next` param or `/`
  - On fail: re-render login with `error` (no redirect, 401)
- `POST /logout` — clear cookie (`MaxAge:-1`), redirect `/login`
- Static assets (`/static/*`) stay public (no auth) for login page styling

### 4. Server Wiring (`internal/server/server.go`)

- `New(cfg)`:
  - `apiAuth := middleware.ApiAuth(cfg.APIKey, cfg.AdminToken)`
  - `adminAuth := middleware.AdminAuth(cfg.AdminToken)`
  - Routes:
    ```
    r.Get("/login", uiHandler.LoginPage)
    r.Post("/login", uiHandler.Login)
    r.Post("/logout", uiHandler.Logout)
    r.Handle("/static/*", static) // public
    r.With(adminAuth).Mount("/", uiHandler.Routes()) // dashboard + partials + /api/*
    r.With(apiAuth).Route("/v1", ...) // /v1/models, /v1/metrics, /v1/chat/completions
    r.With(apiAuth).Post("/v1/messages", ...)
    r.With(apiAuth).Get("/ready", ...)
    ```
  - Note: `uiHandler.Routes()` currently mounts `/`, `/partials/*`, `/api/*` — after wrapping with `adminAuth`, all become admin-only except explicit public above. Ensure mount order: public routes defined before admin mount so they are not shadowed.

### 5. Tests & Docs

- `middleware_test.go`: ApiAuth multi-key (any valid), admin superset, invalid 401, AdminAuth cookie valid/invalid, header fallback, redirect vs JSON, HMAC tamper
- `config_test.go`: comma split, trim, empty drop, admin required, length check
- `ui/handler_test.go`: GET /login 200, POST /login success sets cookie, failure 401, GET / without cookie 302, with cookie 200
- `docs/ENV.md`, `docs/RUNBOOK.md`, `.env.example` updated

## Data Flow

1. Startup: `config.Load()` parses `ADMIN_TOKEN` + `API_KEY` list, `Validate()` enforces admin required.
2. Request:
   - `/v1/*` → `ApiAuth` → check API list or admin → pass to `ChatService`
   - `/` → `AdminAuth` → cookie HMAC verify or header → pass to `uiHandler`
   - `/login` → public, validates token, sets cookie
3. No server-side session store; cookie is stateless HMAC.

## Security

- `crypto/subtle.ConstantTimeCompare` for all token compares
- Cookie `HttpOnly`, `SameSite=Lax`, `Secure` when `X-Forwarded-Proto: https` or `r.TLS != nil`
- Do not log token or cookie value (only `request_id`, `remote`)
- `ADMIN_TOKEN` not in `localStorage`, not in JS (only header/cookie)
- Rate limiter remains per-IP (32 shards), not per-key (YAGNI)

## Error Handling

- `401` JSON for API: `{"error":{"type":"unauthorized","message":"invalid or missing API key"}}` (same as now, but now checks list)
- Dashboard unauth HTML: `302 /login?next=...`; `POST /login` invalid → `200 login.html` with error banner (no redirect loop)
- `500` on HMAC failure (should not happen)

## Testing

- Unit: middleware, config, ui handler
- Integration: `httptest` full router with `TestServer` → login → cookie → dashboard → api
- Manual Chrome DevTools: login form, VPN switch, curl with multi-key, curl with admin token to `/v1/models`

## Alternatives Considered

- Single list where first key is admin: rejected — implicit, error-prone rotation
- JWT: rejected — needs lib, expiry complexity, single admin doesn't need it
- BasicAuth: rejected — browser popup, poor TerminalUI, logout hard

## Open Questions (resolved)

- `ADMIN_TOKEN` required → yes, Validate rejects empty
- Cookie name `fg_admin` → fixed
- `ADMIN_TOKEN` superset for `/v1` → yes

## References

- `internal/config/config.go:56` (current API_KEY)
- `internal/delivery/middleware/middleware.go:111` (current Auth)
- `internal/server/server.go:198` (current wiring)
