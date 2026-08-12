# Runbook

Operational reference for deploying, monitoring, and recovering freegate.

## Deployment

### Local (development)

```bash
make run                  # run server against a local VPNGate supervisor
# or, for the full stack:
make up                   # docker compose up -d (proxy + vpn)
```

### Production / remote

```bash
# 1. Build images
docker compose build

# 2. Configure (.env at repo root or in the shell)
cat > .env <<EOF
VPNGATE_COUNTRY=
VPNGATE_MIN_SCORE=0
API_KEY=$(openssl rand -hex 32)
LOG_LEVEL=info
RATE_LIMIT=60
EOF

# 3. Start
docker compose up -d

# 4. Confirm health
docker compose ps
docker compose logs --tail=200 proxy
```

The compose file is the deployment contract. It pins:

| Service | Image | Port binding | Resources | Depends on |
|---------|-------|--------------|-----------|------------|
| `vpn` | `Dockerfile.vpn` (Go 1.26 build → alpine:3.20 + openvpn) | none (internal only) | 128 MB / 0.5 CPU | — |
| `proxy` | `Dockerfile` (Go 1.26 build → alpine:3.20 runtime) | `127.0.0.1:1234:1234` | 512 MB / 1.0 CPU | `vpn` (healthy) |

Both services are `restart: unless-stopped` and live on the `fg-net` compose network.

The `vpn` service needs a Linux host with `/dev/net/tun` (it runs OpenVPN): the compose file passes the device through and grants `NET_ADMIN` / `NET_RAW`. Docker Desktop (macOS/Windows) does not support TUN/TAP.

### Exposing beyond `127.0.0.1`

The default port binding is local-only. To expose:

1. Edit `docker-compose.yml` and change the `ports:` mapping to your public interface (or remove `127.0.0.1:` prefix)
2. Set `API_KEY` in `.env` — **the dashboard does not require auth, but the API does when `API_KEY` is set**
3. Put a reverse proxy (Caddy, nginx, traefik) in front for TLS

## Health checks

Three layered endpoints, all `GET`, all unauthenticated by default (auth is on `/v1/*` and `/ready` only when `API_KEY` is set):

| Endpoint | Used by | Returns |
|----------|---------|---------|
| `GET /ready` | Docker `HEALTHCHECK` in `Dockerfile`, ops probes | `200 {"status":"ok"}` once models are loaded; `503 {"status":"not ready"}` otherwise |
| `GET /api/health` | Dashboard health badge (refreshed every 3 s) | JSON: `{ok, uptime, started_at, has_models, model_count, vpn_ip}` |
| `GET /api/timeseries` | Dashboard chart (refreshed every 10 s) | Array of `{ts, total_requests, errors, retries, rate_limit_hits, per_upstream}` (1 h rolling, 10 s samples) |

Docker healthchecks:

- **proxy:** `wget --spider http://localhost:1234/ready` (30 s interval, 10 s start period, 3 retries)
- **vpn:** `wget -q -O /dev/null http://127.0.0.1:8080/healthz` (10 s interval, 20 s start period, 5 retries) — 200 once the tunnel is up

Quick manual probe:

```bash
curl -s http://localhost:1234/ready | jq
curl -s http://localhost:1234/api/health | jq
curl -s http://localhost:1234/v1/models | jq '.data | length'  # should be > 0
```

## Metrics

`GET /v1/metrics` returns the live in-memory snapshot:

```json
{
  "total_requests": 1234,
  "retry_count": 12,
  "upstream_errors": 3,
  "rate_limit_hits": 7,
  "total_tokens": 982134,
  "per_upstream": {"opencode": 900, "kilo": 334}
}
```

The same counters are surfaced on the dashboard's stat cards (auto-refresh 5 s) and the recent-requests table (last 100 entries, auto-refresh 5 s). All metrics are **in-memory only** — they reset on restart.

## Common issues

### `models not ready` / `/ready` returns 503

The upstream catalog hasn't loaded yet, or the refreshers failed.

```bash
make logs svc=proxy          # check for "kilo: fetch models" or "opencode: parse models" errors
curl -s http://localhost:1234/v1/models | head
```

If the upstreams are unreachable through the VPN (rare — both have stable public endpoints), check the tunnel:

```bash
docker exec fg-vpn wget -q -O /dev/null https://api.ipify.org && echo ipify-ok
# or from the proxy side, through the SOCKS5 proxy:
docker exec fg-proxy sh -c 'wget -q -O - https://api.ipify.org' 2>/dev/null || echo 'check vpn status'
curl -s http://127.0.0.1:8080/status  # via docker exec fg-vpn if needed
```

### `429 Too Many Requests` on the proxy

The rate limiter is per-IP. Symptoms: a client gets `{"error":{"type":"rate_limit","message":"rate limit exceeded, try again later"}}` with `Retry-After: 60`. Tune via `RATE_LIMIT` (default 60 / min).

The rate limiter is **in-memory only**; restarting the proxy clears all counters.

### Upstream returns 429 → IP rotation

The `ChatService` retries up to 5 times (`defaultMaxRetries` in `internal/server/server.go`) on 429, calling `ForceNewIP` between attempts. If the upstream stays 429, the client sees `MaxRetriesExceededError` and the request is logged with `status: 429`. The metrics counter `retry_count` increments per attempt; `upstream_errors` increments once at the end.

### `502 Bad Gateway` with "select upstream" error

Routing could not find a free upstream for the model. Verify:

1. The model is in `GET /v1/models` (it must be free on either Kilo or OpenCode)
2. `UPSTREAM_DEFAULT` is set to a reachable upstream
3. The `vpn` container is healthy

### Dashboard shows `vpn ip: —`

The VPN IP monitor (`internal/infrastructure/vpngate/controller.go::StartMonitor`) is unable to reach `https://api.ipify.org?format=json` through the SOCKS5 proxy. Common causes:

- `vpn` container not healthy (check `make logs svc=vpn`)
- SOCKS5 port mismatch (verify `VPNGATE_SOCKS_PORT`)

The monitor logs the current IP every 5 minutes. The dashboard polls `/api/health` every 3 s.

### `panic recovered` in logs

The `Recoverer` middleware converts panics into 500 responses with `{"error":{"type":"internal","message":"internal server error"}}`. Capture a stack with `LOG_LEVEL=debug`, then file an issue with the request ID (printed in the log line) and the offending endpoint.

### Body too large

`/v1/chat/completions` and `/v1/messages` reject bodies > 10 MB with `413 {"error":{"type":"body_too_large","message":"request body exceeds 10 MB limit"}}`. The cap is set in `internal/delivery/handler/chat.go::MaxRequestBodySize` — increase there if needed and rebuild.

### Out of memory / OOM kill

The compose file caps `proxy` at 512 MB. If the dashboard ring buffers or the in-memory request log grows beyond this (shouldn't happen — capped at 100 requests + 360 timeseries samples), the container will be killed. Check `docker stats fg-proxy` and `make logs svc=proxy`.

## Rollback procedures

The project ships a single Go binary per release; rollback = deploy the previous image.

### With compose (recommended)

```bash
# Option 1: revert the source and rebuild
git checkout <previous-tag>
docker compose build proxy
docker compose up -d proxy

# Option 2: use a pinned image (after first publishing one to a registry)
# Edit docker-compose.yml to point proxy.image at the previous tag, then:
docker compose up -d proxy
```

### Without compose

```bash
git checkout <previous-tag>
make build           # produces ./server
# stop the running server (SIGTERM is handled with a 10 s graceful shutdown per server.go)
kill $(pgrep -f './server')
./server &
```

State to be aware of:
- **No persistent state.** All counters, request logs, and model caches are in-memory. A rollback to a previous binary does not require data migration.
- **Rate-limit state is lost on restart** — clients will briefly regain full quota.
- **Upstream catalog is re-fetched on startup**, so the first ~1 second of traffic after a rollback may show empty `/v1/models`.

## Configuration changes without restart

Most config requires a restart. The exception is the rate limiter, but it is not currently hot-reloadable. To rotate config:

```bash
# 1. Edit .env
# 2. Restart just the proxy (vpn does not need to restart)
docker compose up -d proxy
# or
make restart svc=proxy
```

The `vpn` sidecar reads its server-selection filters (`VPNGATE_COUNTRY`, `VPNGATE_MIN_SCORE`, `VPNGATE_MAX_PING`) and `VPNGATE_REFRESH_SECONDS` from env. Changing them requires restarting the `vpn` service (and rebuilding if the env var is baked into the image).

## Alerts / escalation

There is no built-in alerting. Recommended external probes:

- **`/ready` (200)** — proxy is serving
- **`/api/health.has_models` (true)** — catalog is loaded
- **`/api/health.vpn_ip` (non-empty, non-`unknown`)** — VPN is routing

Wire these into your existing monitor (Uptime Kuma, Healthchecks.io, Datadog HTTP check, etc.) with a 1–5 minute interval. Paging thresholds:

| Signal | Page on |
|--------|---------|
| `/ready` non-200 for > 2 min | Proxy down or models not loading |
| `upstream_errors / total_requests > 0.5` over 5 min | Upstream is degraded or auth keys expired |
| `vpn_ip` empty / `unknown` for > 10 min | `vpn` container is down or the tunnel is broken |

## Disaster recovery

There is no persistent data to recover. The entire state of a running freegate is reconstructable from:

1. Source code (`git clone`)
2. `.env` (kept in your secret manager, **not** in the repo)
3. Compose file (in the repo)

To rebuild from scratch on a new host:

```bash
git clone <repo>
cd freegate
# restore .env from your secret manager
docker compose up -d
```

## Security checklist (production)

- [ ] `API_KEY` is set to a high-entropy random value
- [ ] `VPNGATE_MIN_SCORE` is set high enough to prefer reputable relays
- [ ] Port `1234` is bound to `127.0.0.1` or behind a reverse proxy with TLS
- [ ] `vpn` container is on an internal network (`fg-net`); SOCKS port is not exposed to the host
- [ ] `LOG_LEVEL` is `info` (not `debug`) in production
- [ ] Docker socket is not mounted into either container
- [ ] `.env` is in `.gitignore` and stored only in a secret manager
