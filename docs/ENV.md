# Environment Variables

freegate is configured entirely through environment variables. Defaults are shown in the **Default** column; an empty `Default` means the variable has no built-in default and the value is either required at runtime, derived (e.g. `SOCKSAddr` = `VPNGATE_HOST:VPNGATE_SOCKS_PORT`), or simply unset.

The authoritative list lives in `internal/config/config.go::Load`; this file is generated from it and `.env.example`. If you change one, change the other.

<!-- AUTO-GENERATED -->

## Server

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `PORT` | No | `1234` | Port the proxy binds on (`0.0.0.0:<PORT>`) |
| `LOG_LEVEL` | No | `info` | Log verbosity: `debug`, `info`, `warn`, `error` (slog level) |
| `ADMIN_TOKEN` | Yes | (empty) | **Required**, >=6 chars (user-defined password). Gates dashboard (`/`, `/partials/*`, `/api/*`, `/api/vpn/*`) via `AdminAuth` (cookie `fg_admin` HMAC-SHA256 or header `X-Admin-Token` / `Authorization: Bearer`). Also valid as superset for `/v1/*` — raw token or the post-login `fg_admin` session cookie both work. `GET /ready` is public (no token) for Docker HEALTHCHECK. Generate: `openssl rand -hex 32` or any password >=6. Compared with `subtle.ConstantTimeCompare`. |
| `API_KEY` | No | (empty) | Comma-separated list, e.g. `key1,key2`. Any entry valid for `/v1/*`, `/v1/messages`, `/v1/metrics` via `ApiAuth` (`X-API-Key` or `Authorization: Bearer`). `ADMIN_TOKEN` is also valid there (superset). Empty = no API auth (admin still required for dashboard). Entries are trimmed; empty entries dropped. |
| `RATE_LIMIT` | No | `60` | Requests per minute per client IP (sharded 32-way, `RateLimiter` per-IP map). Returning clients (within 2 min) get HTTP 429 with `Retry-After: 60` and a JSON error body. |

## VPN (single-binary per-OS)

freegate now runs as a **single binary** with embedded VPNGate per OS (linux/darwin/windows). The binary auto-detects `runtime.GOOS`, probes for `openvpn` (`openvpn.exe` on Windows, `/opt/homebrew/bin/openvpn` on macOS) and starts an in-process OpenVPN tunnel + SOCKS5 on `127.0.0.1:9050`. If `openvpn` is missing or `tun` permission fails, it falls back to **direct** automatically. Toggle live from the dashboard or via `VPN_ENABLED=false` / `--vpn=false`.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `VPN_ENABLED` | No | `true` | Enable embedded VPN. `false` = direct connections, no tunnel. Also overridable via `--vpn=false` flag. |
| `VPN_PROVIDER` | No | `auto` | VPN provider: `auto` (GOOS-aware), `vpngate`, or `direct`. |
| `VPNGATE_SOCKS_PORT` | No | `9050` | SOCKS5 port for in-process tunnel (127.0.0.1:9050) |
| `VPNGATE_CTRL_PORT` | No | `8080` | Deprecated: legacy sidecar control port (kept for docker compat) |
| `VPNGATE_ROTATE_INTERVAL` | No | `30` | Minimum seconds between scheduled IP rotations (`NewIP`). `ForceNewIP` (dashboard rotate button) bypasses it. |
| `VPNGATE_HOST` | No | `127.0.0.1` | Deprecated: docker sidecar host. If set (e.g. `vpn` in compose), `SOCKSAddr` honors it; otherwise 127.0.0.1. |

The internal `SOCKSAddr` field is derived as `127.0.0.1:VPNGATE_SOCKS_PORT` when `VPN_ENABLED=true` (or `VPNGATE_HOST:VPNGATE_SOCKS_PORT` if `VPNGATE_HOST` is explicitly set for docker compat); empty when direct. Helpers `Config.IsDirect()` and `Config.IsSidecarMode()` centralize this check (replaces scattered `CurrentIP()=="direct"` string compares).

`VPNGATE_COUNTRY` / `VPNGATE_MIN_SCORE` / `VPNGATE_MAX_PING` filters are now applied in-process by the Provider (`vpn/registry.go` via `matchCountry`/`pickWeighted`); no sidecar env needed.

`VPNGATE_COUNTRY` accepts a country name or ISO code (e.g. `Korea Republic of` or `KR`), or a `!`-prefixed exclusion (e.g. `!Japan` to use every country except Japan). Empty (the default) offers every relay in the dashboard picker, including Japan.

## Upstreams

### OpenCode (default)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `UPSTREAM_URL_OPENCODE` | No | `https://opencode.ai/zen/v1` | OpenCode base URL |
| `UPSTREAM_KEY_OPENCODE` | No | `public` | Bearer token attached to every OpenCode request. OpenCode also gets an `x-opencode-client: desktop` header. |
| `UPSTREAM_OPENCODE_FREE_ALLOWLIST` | No | `big-pickle` | Comma-separated model IDs that are free on OpenCode but don't follow the `-free` naming convention. Default includes `big-pickle` (served as deepseek-v4-flash with cost 0). |
| `UPSTREAM_REFRESH_OPENCODE` | No | `60` | How often to refresh the OpenCode `/models` catalog (seconds) |

### Kilo (OpenRouter)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `UPSTREAM_URL_KILO` | No | `https://api.kilo.ai/api/openrouter` | Kilo / OpenRouter base URL |
| `UPSTREAM_KEY_KILO` | No | `anonymous` | Bearer token attached to every Kilo request |
| `UPSTREAM_REFRESH_KILO` | No | `60` | How often to refresh the Kilo `/models` catalog (seconds) |

### LLM7 (keyless gateway)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `UPSTREAM_URL_LLM7` | No | `https://api.llm7.io/v1` | LLM7 keyless gateway base URL (`api.llm7.io/v1`). Any non-empty bearer works; freegate sends `unused`. |
| `UPSTREAM_REFRESH_LLM7` | No | `300` | How often to refresh the LLM7 `/models` catalog (seconds). Last-known models stay cached on failure. Frequency is lower (300s) to reduce pressure on free relays. |

### Routing

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `UPSTREAM_DEFAULT` | No | `opencode` | Fallback upstream for models that don't appear in Kilo's free catalog. Accepts `opencode` or `kilo`. |

## Validation

`config.Validate()` is called at startup. It rejects:
- Empty or `<6 chars` `ADMIN_TOKEN` (`ADMIN_TOKEN is required`, `ADMIN_TOKEN must be at least 6 characters`)
- `API_KEY` entries that are empty/whitespace after comma-split (`API_KEY entries must be non-empty`)
- Empty `UPSTREAM_URL_OPENCODE`, `UPSTREAM_URL_KILO`, or `UPSTREAM_URL_LLM7`
- Empty `SOCKSAddr` when `VPN_ENABLED=true` and `VPN_PROVIDER != "direct"` (helper `IsDirect()` is single source)
- Invalid `VPN_PROVIDER` (must be `auto`, `vpngate`, or `direct`)
- `PORT` outside `1–65535`; `VPNGATE_SOCKS_PORT` outside `1–65535` only when `VPN_ENABLED=true`; `VPNGATE_CTRL_PORT` outside `1–65535` only in sidecar mode (`IsSidecarMode()`); `VPNGATE_ROTATE_INTERVAL` non-positive only when `VPN_ENABLED=true`; `RATE_LIMIT` non-positive always

A failure prints a multi-line error and exits 1.

Dashboard auth is via `AdminAuth` cookie `fg_admin` = `HMAC-SHA256(ADMIN_TOKEN, ADMIN_TOKEN)` hex (or header `X-Admin-Token`/`Bearer`); API auth is via `ApiAuth(apiKeys, adminToken)` checking each `API_KEY` entry then `ADMIN_TOKEN` superset with `subtle.ConstantTimeCompare`. Unauthenticated dashboard HTML redirects `302 /login?next=...`; HTMX/JSON gets `401 {"error":{"type":"unauthorized"}}`.

## Source-of-truth files

- `internal/config/config.go` — `Config` struct, `Load()`, `Validate()`, helpers `IsDirect()`/`IsSidecarMode()`
- `internal/infrastructure/vpn/` — `provider.go` + `registry.go` (server list cache) + `tunnel.go` (OpenVPN lifecycle) + `socks.go` per-OS
- `internal/infrastructure/upstream/` — `client.go` (`NewTransport` shared, `NewHTTPClientWithTransport`), `cache.go` (O(1) `Has`), `upstream.go` (`Upstream = domain.Upstream`)
- `internal/domain/` — `upstream.go` + `response.go` (`UpstreamResponse` decouples `net/http`), `model.go` canonical
- `internal/translate/internal/prepost/prepare_upstream.go` — one-pass `PrepareUpstream` (roles+reasoning+stream_options)
- `internal/infrastructure/proxy/normalize.go` — `NormalizeDomainResponseWithContext` (ctx-aware streaming), `RateLimiter` sharded 32-way in `internal/delivery/middleware`
- `.env.example` — annotated example
- `docker-compose.yml` — legacy containerized path (proxy + vpn sidecar)
- `cmd/vpngate-supervisor/main.go` — legacy sidecar (deprecated, kept for docker compat)

<!-- /AUTO-GENERATED -->
