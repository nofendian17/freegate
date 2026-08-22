# Environment Variables

freegate is configured entirely through environment variables. Defaults are shown in the **Default** column; an empty `Default` means the variable has no built-in default and the value is either required at runtime, derived (e.g. `SOCKSAddr` = `VPNGATE_HOST:VPNGATE_SOCKS_PORT`), or simply unset.

The authoritative list lives in `internal/config/config.go::Load`; this file is generated from it and `.env.example`. If you change one, change the other.

<!-- AUTO-GENERATED -->

## Server

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `PORT` | No | `1234` | Port the proxy binds on (`0.0.0.0:<PORT>`) |
| `LOG_LEVEL` | No | `info` | Log verbosity: `debug`, `info`, `warn`, `error` (slog level) |
| `API_KEY` | No | (empty) | If non-empty, every `/v1/*` and `/ready` request must send a matching `Authorization: Bearer <key>` or `X-API-Key: <key>` header. Empty = no auth. |
| `RATE_LIMIT` | No | `60` | Requests per minute per client IP. Returning clients (within 2 min) get HTTP 429 with `Retry-After: 60` and a JSON error body. |

## VPNGate (proxy)

freegate routes all upstream traffic through a VPNGate/OpenVPN tunnel provided by the `vpn` sidecar container (`cmd/vpngate-supervisor`). The supervisor exposes a SOCKS5 proxy and a small HTTP control API (`/rotate`, `/connect`, `/servers`, `/status`, `/ip`) used by the dashboard's manual server picker. There is no automatic IP rotation on upstream 429s — pick a server manually from the dashboard, or switch to **direct** (no tunnel, from the proxy container) with the "direct (no VPN)" option. There is no static bypass env var; the route is switched live at runtime.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `VPNGATE_HOST` | No | `127.0.0.1` | SOCKS5 / control host. In docker-compose this is set to the `vpn` service name. |
| `VPNGATE_SOCKS_PORT` | No | `9050` | SOCKS5 port used for all upstream traffic |
| `VPNGATE_CTRL_PORT` | No | `8080` | Supervisor control API port (`POST /rotate`, `GET /ip`) |
| `VPNGATE_ROTATE_INTERVAL` | No | `30` | Minimum seconds between scheduled IP rotations (`NewIP`). `ForceNewIP` (dashboard rotate button) bypasses it. |

The internal `SOCKSAddr` field is derived as `VPNGATE_HOST:VPNGATE_SOCKS_PORT`.

The `vpn` sidecar reads its own env vars: `VPNGATE_COUNTRY`, `VPNGATE_MIN_SCORE`, `VPNGATE_MAX_PING` (server-selection filters) and `VPNGATE_REFRESH_SECONDS` (server-list refresh interval). These are wired in `docker-compose.yml`.

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
- Empty `UPSTREAM_URL_OPENCODE`, `UPSTREAM_URL_KILO`, or `UPSTREAM_URL_LLM7`
- `PORT`, `VPNGATE_SOCKS_PORT`, or `VPNGATE_CTRL_PORT` outside `1–65535`
- Non-positive `VPNGATE_ROTATE_INTERVAL` or `RATE_LIMIT`

A failure prints a multi-line error and exits 1.

## Source-of-truth files

- `internal/config/config.go` — `Config` struct, `Load()`, `Validate()`
- `.env.example` — annotated example
- `docker-compose.yml` — wires these into the `proxy` and `vpn` services
- `cmd/vpngate-supervisor/main.go` — the `vpn` sidecar (OpenVPN tunnel + SOCKS5 + control API)

<!-- /AUTO-GENERATED -->
