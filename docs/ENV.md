# Environment Variables

freegate is configured entirely through environment variables. Defaults are shown in the **Default** column; an empty `Default` means the variable has no built-in default and the value is either required at runtime or simply unset.

The authoritative list lives in `internal/config/config.go::Load`; this file is generated from it and `.env.example`. If you change one, change the other.

<!-- AUTO-GENERATED -->

## Server

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `PORT` | No | `1234` | Port the proxy binds on (`0.0.0.0:<PORT>`) |
| `LOG_LEVEL` | No | `info` | Log verbosity: `debug`, `info`, `warn`, `error` (slog level) |
| `ADMIN_TOKEN` | Yes | (empty) | **Required**, >=6 chars (user-defined password). Gates dashboard (`/`, `/partials/*`, `/api/*`) via `AdminAuth` (cookie `fg_admin` — a 12 h signed session set by `POST /login` — or header `X-Admin-Token` / `Authorization: Bearer`). Also valid as superset for `/v1/*` — raw token or the post-login `fg_admin` session cookie both work. `GET /ready` is public (no token) for Docker HEALTHCHECK. Generate: `openssl rand -hex 32` or any password >=6. Compared with `subtle.ConstantTimeCompare`; failed logins and rejected credential attempts are throttled per IP with exponential backoff. Client API keys are DB-managed (`POST /api/api-keys`, managed at `/settings`); no env var needed. |
| `RATE_LIMIT` | No | `60` | Requests per minute per client IP (sharded 32-way, `RateLimiter` per-IP map). Returning clients (within 2 min) get HTTP 429 with `Retry-After: 60` and a JSON error body. |
| `MAX_REQUEST_BODY_MB` | No | `30` | Cap for `/v1/*` request bodies in MB. Larger bodies are rejected with `413 body_too_large` before being fully buffered. Must be positive. |
| `TRUST_PROXY_HEADERS` | No | `false` | Honor `X-Forwarded-For` / `X-Real-IP` when deriving the client IP (rate limit buckets, logs, request history). Leave `false` when exposed directly — forwarded headers are client-controlled and spoofable. Set `true` only behind a reverse proxy that overwrites these headers. |

## Proxy pools (SQLite)

Upstream requests route through enabled proxy pools (`GET/POST/PUT/DELETE /api/pools`, managed at `/providers`): each pool is a Vercel edge relay URL, applied round-robin with `x-relay-target` / `x-relay-path` headers; `no_proxy` bypasses per-pool. Disabling a pool removes it from the global rotation, and an upstream pinned to that pool connects directly instead of failing over to another relay. Pool changes rebuild live — no restart, no env vars.

## Upstreams

### OpenCode (default)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `UPSTREAM_URL_OPENCODE` | No | `https://opencode.ai/inference/v1` | OpenCode base URL (legacy `/zen/v1` values are auto-migrated to `/inference/v1`; free inference moved there on 2026-10-14, 9router PR #4842) |
| `UPSTREAM_OPENCODE_BUN_TLS` | No | `true` | Present Bun 1.4.4's TLS ClientHello (uTLS) to the configured OpenCode host. The free-tier gate allowlists the Bun handshake and rejects stock Go TLS with 429 `FreeUsageLimitError` even when the HTTP request is byte-identical. Relay hosts reached through the same client keep standard Go TLS. Set `false` only as a kill switch if upstream stops requiring the Bun fingerprint. |
| `UPSTREAM_KEY_OPENCODE` | No | `public` | Bearer token attached to every OpenCode request. No `x-api-key` is sent (the genuine client authenticates with `Authorization: Bearer` only). Requests also carry `x-opencode-client: cli`, canonical per-request `x-opencode-session` (+ mirrored `x-opencode-session-id`/`x-session-affinity`/`x-session-id`), `x-opencode-project: global`, v2 first-party `User-Agent` (`opencode/latest/<version>/cli`), and `anthropic-version: 2023-06-01` for Messages models (per 9router PR #4842). Anonymous requests get OpenCode's exact `read`/`shell` fingerprint appended per endpoint shape (verbatim v2.0.26 descriptions/schemas; conflicting caller tools are aliased to `*_client` and restored on response), because the gateway rejects stub descriptions, renamed schemas, and `tool_choice: none` with 403 `FreeTierError`. They also get a minimal OpenCode-style `<env>` block in the system prompt/instructions when the caller does not supply one: live probes show the gate answers 429 `FreeUsageLimitError` without it even when the fingerprints are present. Downstream `User-Agent` / `x-opencode-*` headers are ignored; freegate mints fresh canonical identity per request (`cli` client, `global` project, fresh `ses_` ID, no `x-opencode-request`). |
| `OPENCODE_CLIENT_VERSION` | No | _(auto)_ | Pins the `opencode/latest/<version>/cli` User-Agent sent to the inference gateway. Empty = track the latest OpenCode release (refreshed every 12h, falls back to `2.0.26` offline). The release probe is fail-open and quiet (failures log at debug only), and is skipped entirely when pinned. The gateway requires v2 (`major >= 2`); v1 `opencode/1.18.x` is rejected with 403 since 2026-10-14. |
| `UPSTREAM_OPENCODE_FREE_ALLOWLIST` | No | `big-pickle` | Comma-separated model IDs that are free on OpenCode but don't follow the `-free` naming convention. Default includes `big-pickle` (served as deepseek-v4-flash with cost 0). |
| `UPSTREAM_REFRESH_OPENCODE` | No | `60` | How often to refresh the OpenCode `/models` catalog (seconds) |

Relay limitation: `UPSTREAM_OPENCODE_BUN_TLS` applies only to the direct connection to the configured OpenCode host. Relay hosts keep standard Go TLS, and the relay's own connection to OpenCode is outside freegate's control. A relay whose egress TLS fingerprint, IP, or quota does not satisfy OpenCode's gate still gets 429 `FreeUsageLimitError` for anonymous free-tier requests. Use direct mode for the free tier, or a relay that forwards with a compatible handshake.

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
| `UPSTREAM_DEFAULT` | No | `opencode` | Fallback upstream for models claimed by nothing else. Accepts `opencode`, `kilo`, or `llm7`. |
| `RESPONSE_MODELS` | No | `muse-spark,muse_spark` | Comma-separated substrings (case-insensitive) routing models to the OpenAI Responses API (`/inference/v1/responses`). |
| `MESSAGE_MODELS` | No | `union-alpha` | Comma-separated substrings (case-insensitive) routing models to the Anthropic Messages API (`/inference/v1/messages`). |
| `PROVIDERS_DB_PATH` | No | `./data/providers.db` | SQLite file (GORM, pure-Go, CGO-free) holding custom providers, tiered combos, and — once seeded — auth + upstream settings. Auto-created on boot; local files are restricted to `0600` and their directory to `0700`. Persist it: mount a volume over `./data` in docker; back up the file before upgrades. |

## Custom providers + tiered combos (SQLite)

Built-in upstreams (opencode/kilo/llm7) stay env-configured. User providers live in `PROVIDERS_DB_PATH`:

- **Custom providers** — any OpenAI-compatible base URL + API keys + optional headers + explicit model selection (checkboxes from the `test` probe; only stored models route, refresh only updates their metadata) + per-provider refresh interval, managed at `/providers` (dashboard, admin-only) or `GET/POST/PUT/DELETE /api/providers` (+ `POST /api/providers/{id}/test` on the stored row, `POST /api/providers/probe` ad-hoc on form values pre-save; the probe sends custom headers too). Keys are masked on read (last-4). Rows saved before the selection feature have `models: null` (legacy: whole catalog routes); probing such a row pre-checks the full catalog so the first save preserves routing. `PUT` without a `models` key keeps the stored selection; an explicit `[]` routes nothing.
- **Tiered combos** — each combo is a virtual model: `{"name":"hemat","tiers":[{"provider":"opencode"},{"provider":"custom:acme"}]}`. Request `model=hemat` and tiers are tried in order; transport errors, 429s, and 5xx fail over to the next tier (body sent as-is, failed tier bodies closed). Other 4xx (e.g. 401) pass through verbatim by design. Combos appear in `GET /v1/models` with `provider: combo:<name>`. Managed at `/providers` or `GET/POST/PUT/DELETE /api/combos` (+ `POST /api/combos/{id}/test` per-tier probe). No activate step — every combo is always routable.
- **Live rebuild** — provider/combo CRUD rebuilds upstreams synchronously (failure → 500, old chain kept). No restart needed. Deleted providers are pruned from combo tiers (combos left with zero tiers are deleted).

## Debugging

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `UPSTREAM_CAPTURE` | No | `false` | Raw upstream response logging via slog. When `true`, every upstream response line is printed to stdout/stderr as `INFO msg="upstream raw response"` carrying `request_id` and `model` — one record per SSE line, including streams that end without `[DONE]`/`finish_reason`. Also logs each outgoing inference request as `INFO msg="upstream opencode request"` with endpoint, wire headers, and body (credentials always redacted except the anonymous `public` markers). Lines contain full conversation content: enable only while debugging, on trusted machines. Wired in `server.go` → `ChatService.WithRawUpstreamLog` → `application/rawupstream.go::rawLineLogger`; request side in `upstream/logOpenCodeRequest`. |

Related (no env needed): degenerate upstream responses — HTTP 200 with no content/tool calls at all — are always logged as `WARN msg="upstream empty completion"` with `model` + `request_id`, regardless of this switch.

## Validation

`config.Validate()` is called at startup. It rejects:
- Empty or `<6 chars` `ADMIN_TOKEN` (`ADMIN_TOKEN is required`, `ADMIN_TOKEN must be at least 6 characters`)
- Empty `UPSTREAM_URL_OPENCODE`, `UPSTREAM_URL_KILO`, or `UPSTREAM_URL_LLM7`
- `PORT` outside `1–65535`; `RATE_LIMIT` non-positive always

A failure prints a multi-line error and exits 1.

Dashboard auth is via `AdminAuth` cookie `fg_admin` = `nonce.expiry.HMAC-SHA256(ADMIN_TOKEN, nonce|expiry)` (hex, 12 h TTL, fresh value per login) set by `POST /login` (or header `X-Admin-Token`/`Bearer`); API auth is via `ApiAuthDB(adminToken, checker)` checking the `ADMIN_TOKEN` superset then DB-managed client keys with `subtle.ConstantTimeCompare`. Unauthenticated dashboard HTML redirects `302 /login?next=...`; HTMX/JSON gets `401 {"error":{"type":"unauthorized"}}`. Without any client key, only the post-login `fg_admin` cookie or raw `ADMIN_TOKEN` header grants `/v1/*` access — there is no open API. Rejected credential attempts (login form or `X-Admin-Token`/`Bearer`) are throttled per IP with exponential backoff.

## Source-of-truth files

- `internal/config/config.go` — `Config` struct, `Load()`, `Validate()`
- `internal/infrastructure/upstream/` — `client.go` (`NewTransport` shared, `NewHTTPClientWithTransport`), `relay.go` (edge-relay `SharedRelay`, `x-relay-target` / `x-relay-path`), `cache.go` (O(1) `Has`), `upstream.go` (`Upstream = domain.Upstream`)
- `internal/domain/` — `upstream.go` + `response.go` (`UpstreamResponse` decouples `net/http`), `model.go` canonical
- `internal/translate/internal/prepost/prepare_upstream.go` — one-pass `PrepareUpstream` (roles+reasoning+stream_options)
- `internal/infrastructure/proxy/normalize.go` — `NormalizeDomainResponseWithContext` (ctx-aware streaming), `RateLimiter` sharded 32-way in `internal/delivery/middleware`
- `internal/application/rawupstream.go` — `rawLineLogger` (opt-in per-line raw upstream log, `UPSTREAM_CAPTURE=true`)
- `.env.example` — annotated example
- `docker-compose.yml` — single proxy container

<!-- /AUTO-GENERATED -->
