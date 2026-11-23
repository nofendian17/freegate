# Changelog

All notable changes to freegate are documented here. Releases before
v0.22.0 are documented in [GitHub Releases](https://github.com/nofendian17/freegate/releases).

## [Unreleased]

### Fixed

- A WARP rotation whose replacement failed no longer stalls the retry for the
  full `WARP_ROTATE_WAIT`. Rotation now waits on the replacement attempt
  itself, so a fast registration failure returns immediately (the restored
  exit is retried without a 30s pause).
- An explicit `WARP_SOURCE_IPV6` is validated like auto-detection: ULA,
  link-local, documentation and other non-public prefixes are rejected at
  startup/API time instead of binding an unroutable source.
- WARP DNS no longer runs inside the tunnel. Hostnames are resolved with the
  host resolver and the tunnel dials the resulting literal address, so a
  dropped in-tunnel UDP query to `2606:4700:4700::1111` cannot fail every
  request on an exit. The tunnel resolver remains a fallback.
- Admin pool tests report a successful status only when an exit has a public
  egress address.

### Changed

- The default WARP stack is now `dual`: IPv6-capable upstreams still use the
  exit's own /64, while A-only hosts such as `api.kilo.ai` are reachable
  again. The pool form and docs offer `dual` and `ipv6`; `ipv4` is rejected
  because Cloudflare's shared IPv4 egress cannot be rotated.
- Removed the unused response-based rotation path (`Report`,
  `ReportWithRetryAfter`, `HasUntriedActive`); tests now exercise the same
  `Fail` path production uses.
- WARP startup logs once (with resolved source, preload and fallback) instead
  of repeating the pool summary in three layers.

## [0.23.0] - 2026-11-24

### Added

- WARP egress is now a first-class routing target. Create `warp` pools under
  Providers → Proxy pools and pin any provider or builtin to one; pool edits
  apply on the next rebuild without a restart. `GET /api/egress` reports each
  exit's ID, public egress address, /64 prefix and status.
- WARP runs **in-process** (userspace WireGuard + gVisor netstack). Each exit is
  an independently registered Cloudflare device; no sidecars, `NET_ADMIN`, TUN
  device, or per-container registration volumes are needed.
- Each exit binds its WireGuard socket to a random address from a routed
  `WARP_SOURCE_IPV6` prefix, so a replacement presents a new outer source and
  receives a genuinely different /64 quota bucket. The prefix defaults to
  `auto`, which discovers a public routed prefix from the host; an explicit
  `/64` still overrides detection. When auto-detection finds no public prefix,
  WARP now runs without source rotation and logs a warning instead of refusing
  to start.
- The exit pool scales with demand (preload half of `WARP_POOL_SIZE`, hard
  ceiling at the configured size), cools a rejected /64 for 30 minutes, and
  honors Cloudflare's registration `Retry-After` with backoff.

### Changed

- The default WARP stack is now `dual`: IPv6-capable upstreams still use the
  exit's own /64, while A-only hosts such as `api.kilo.ai` are reachable
  again. The pool form and docs offer `dual` and `ipv6`; `ipv4` is rejected
  because Cloudflare's shared IPv4 egress cannot be rotated.
- WARP rotation retires the exit that served the request. A per-request lease
  stays alive until the response body is closed, so a rotating exit is never
  released while an SSE stream is still using it; the retry waits for a fresh
  exit instead of polling a timeout. Kilo and LLM7 now rotate on any 429 as
  well (their limits are per address); per-key 429s still use key cooldown.
- WARP transports pin HTTP/1.1 and disable keep-alives so a pooled connection
  can never be reused against a retired exit.
- `EGRESS_WARP_POOL`, `EGRESS_COOLDOWN`, `EGRESS_ROTATE_TIMEOUT` and
  `EGRESS_DRAIN_TIMEOUT` were replaced by `WARP_STACK`, `WARP_POOL_SIZE`,
  `WARP_SOURCE_IPV6`, `WARP_REGISTRATION_TIMEOUT` and `WARP_ROTATE_WAIT`.
  The warp pool form in the admin UI uses the same fields.
- WARP variables now live in the single `docker-compose.yml`;
  `docker-compose.warp.yml` was removed. Enable WARP with `EGRESS_MODE=warp`.
- Devices are not persisted: a restart re-registers exits, so Cloudflare's
  registration rate limit can delay startup (bounded by
  `WARP_REGISTRATION_TIMEOUT`).
- `EGRESS_FALLBACK_DIRECT` defaults to `false`. A dead WARP pool fails closed
  instead of leaking the host IP; opt in explicitly when availability matters
  more than the exit address.
- Pool selection and transports remain one atomic routing snapshot per request.
  Egress is a concrete route, not an interface, so a missing configuration can
  no longer hide behind a typed nil and dial the host address.

### Fixed

- A rotation that returned a new IP in the **same /64 as the exiting device's
  previous address** was accepted: the IP changed but the quota bucket did not.
  Registration now discards exits whose /64 is already active or cooling.
- Docker builds use BuildKit cache mounts for Go modules and compiled packages,
  compile natively on the build platform for any target arch, and ship a much
  smaller build context.

## [0.22.0] - 2026-11-18

### Added

- OpenCode upstream prefers IPv6 and falls back to IPv4 automatically when the
  host has no usable IPv6 route. The first failure is remembered per client
  until restart, so IPv4-only deployments do not retry on every request. Relays,
  proxy pools, IP-literal URLs, and the other upstreams are unaffected.
- Docker Compose enables IPv6 on the service network (Docker assigns the ULA),
  so the container can use the preferred path instead of always falling back.

### Changed

- The default WARP stack is now `dual`: IPv6-capable upstreams still use the
  exit's own /64, while A-only hosts such as `api.kilo.ai` are reachable
  again. The pool form and docs offer `dual` and `ipv6`; `ipv4` is rejected
  because Cloudflare's shared IPv4 egress cannot be rotated.
- The direct OpenCode connection no longer follows the OS dual-stack default:
  IPv6 is attempted first, with IPv4 as the fallback. No configuration is
  required; `UPSTREAM_OPENCODE_NETWORK` does not exist.

[0.22.0]: https://github.com/nofendian17/freegate/releases/tag/v0.22.0
