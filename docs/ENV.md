# Environment Variables

freegate is configured entirely through environment variables. Defaults are shown in the **Default** column; an empty `Default` means the variable has no built-in default and the value is either required at runtime, derived (e.g. `SOCKSAddr` = `TOR_HOST:PORT`), or simply unset.

The authoritative list lives in `internal/config/config.go::Load`; this file is generated from it and `.env.example`. If you change one, change the other.

<!-- AUTO-GENERATED -->

## Server

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `PORT` | No | `1234` | Port the proxy binds on (`0.0.0.0:<PORT>`) |
| `LOG_LEVEL` | No | `info` | Log verbosity: `debug`, `info`, `warn`, `error` (slog level) |
| `API_KEY` | No | (empty) | If non-empty, every `/v1/*` and `/ready` request must send a matching `Authorization: Bearer <key>` or `X-API-Key: <key>` header. Empty = no auth. |
| `RATE_LIMIT` | No | `60` | Requests per minute per client IP. Returning clients (within 2 min) get HTTP 429 with `Retry-After: 60` and a JSON error body. |

## Tor

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `TOR_HOST` | No | `127.0.0.1` | SOCKS5 / control host. In docker-compose this is set to the `tor` service name. |
| `TOR_PORT` | No | `9050` | SOCKS5 port used for all upstream traffic |
| `TOR_CTRL_PORT` | No | `9051` | Tor control port (used for `SIGNAL NEWNYM` on 429 retries) |
| `TOR_PASS` | No | (empty) | Tor control password. The `entrypoint-tor.sh` script generates one randomly if unset. |

The internal `SOCKSAddr` field is derived as `TOR_HOST:TOR_PORT`.

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

### Routing

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `UPSTREAM_DEFAULT` | No | `opencode` | Fallback upstream for models that don't appear in Kilo's free catalog. Accepts `opencode` or `kilo`. |

## Validation

`config.Validate()` is called at startup. It rejects:
- Empty `UPSTREAM_URL_OPENCODE` or `UPSTREAM_URL_KILO`
- `PORT`, `TOR_PORT`, or `TOR_CTRL_PORT` outside `1–65535`
- Non-positive `RATE_LIMIT`

A failure prints a multi-line error and exits 1.

## Source-of-truth files

- `internal/config/config.go` — `Config` struct, `Load()`, `Validate()`
- `.env.example` — annotated example
- `docker-compose.yml` — wires these into the `proxy` and `tor` services
- `entrypoint-tor.sh` — auto-generates a `TOR_PASS` if none is provided

<!-- /AUTO-GENERATED -->
