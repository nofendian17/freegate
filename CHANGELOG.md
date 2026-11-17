# Changelog

All notable changes to freegate are documented here. Releases before
v0.22.0 are documented in [GitHub Releases](https://github.com/nofendian17/freegate/releases).

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
