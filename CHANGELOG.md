# Changelog

All notable changes to freegate are documented here. Releases before
v0.22.0 are documented in [GitHub Releases](https://github.com/nofendian17/freegate/releases).

## [Unreleased]

### Added

- WARP egress is now a first-class routing target. Create `warp` pools under
  Providers → Proxy pools and pin any provider or builtin to one; pool edits
  apply on the next rebuild without a restart. `GET /api/egress` reports live
  endpoint status, exit IP, and rotation counters.
- WARP rotation is attributed to the exact endpoint that served the dial, via a
  per-request lease. A burst of 429s rotates one sidecar instead of whichever
  exit dialed last. The retry waits for the rotation to finish through an
  explicit channel instead of polling a timeout.
- `EGRESS_DRAIN_TIMEOUT` (default `60s`) bounds how long a rotation waits for
  in-flight requests on a sidecar before giving up.
- WARP exit addresses are probed via `GET /ip` and reported with their IPv6 /64
  in `GET /api/egress`. Sidecars sharing a /64 are flagged with
  `duplicate_prefix`, since they share one free-tier quota.

### Changed

- WARP rotation now drains in-flight connections before asking the sidecar to
  change identity. Since WARP tears down the whole tunnel, rotating while a
  request is streaming cut that request off. If the drain exceeds
  `EGRESS_DRAIN_TIMEOUT`, the rotation is skipped and the 429 is returned
  rather than interrupting a live stream.
- A rotation that lands in a /64 already used by another sidecar is retried
  (up to 3 identities) so a shared quota does not silently waste the attempt.
  Documentation now consistently describes the free tier as per /64.

### Fixed

- A rotation that returned a new IP address in the **same /64 as the exiting
  sidecar's previous address** was accepted: the exact IP changed but the
  free-tier quota bucket did not, so the retry still got 429. The prefix check
  now includes the endpoint's own pre-rotation address, and the rotation log
  reports both `previous` and `prefix`.
- Docker builds use BuildKit cache mounts for Go modules and compiled packages,
  compile natively on the build platform for any target arch, and ship a much
  smaller build context. The WARP override builds its shared image once instead
  of once per replica.

- `EGRESS_FALLBACK_DIRECT` now defaults to `false`. A dead WARP pool fails
  closed instead of leaking the host IP; opt in explicitly when availability
  matters more than the exit address.
- `EGRESS_MODE=warp` no longer requires `EGRESS_WARP_POOL` when pools are
  managed in the database. An unpinned WARP selection fails closed instead of
  dialing the host address.
- Pool selection and transports are one atomic routing snapshot per request.
  Egress is a concrete route, not an interface, so a missing configuration can
  no longer hide behind a typed nil and dial the host address.

## [0.22.0] - 2026-11-18

### Added

- OpenCode upstream prefers IPv6 and falls back to IPv4 automatically when the
  host has no usable IPv6 route. The first failure is remembered per client
  until restart, so IPv4-only deployments do not retry on every request. Relays,
  proxy pools, IP-literal URLs, and the other upstreams are unaffected.
- Docker Compose enables IPv6 on the service network (Docker assigns the ULA),
  so the container can use the preferred path instead of always falling back.

### Changed

- The direct OpenCode connection no longer follows the OS dual-stack default:
  IPv6 is attempted first, with IPv4 as the fallback. No configuration is
  required; `UPSTREAM_OPENCODE_NETWORK` does not exist.

[0.22.0]: https://github.com/nofendian17/freegate/releases/tag/v0.22.0
