# Single-Binary VPNGate per-OS — Design Spec

- **Date:** 2026-08-22
- **Branch:** `feat/vpngate-refactor` → `feat/single-binary-vpn`
- **Classification:** Architectural (docker sidecar → single binary + per-OS OpenVPN)
- **Approach chosen:** #1 `runtime.GOOS` switch + `exec openvpn` per OS, SOCKS in-process

## 1. Context & Goal

Current: `docker-compose.yml` runs 2 services — `vpn` (`Dockerfile.vpn` + `cmd/vpngate-supervisor` with OpenVPN `tun0`, `go-socks5`, control API `:8080`) and `proxy` (`internal/server` + `vpngate.Controller` HTTP client).

Goal: single binary `freegate` for `linux+darwin+windows` that auto-detects OS (`runtime.GOOS`) and embeds VPN provider, no Docker required. Docker stays as optional legacy.

## 2. Architecture

### 2.1 Top-level

```
cmd/server/main.go          // single entrypoint
internal/infrastructure/vpn/
  provider.go              // interface Provider
  provider_linux.go        // GOOS=linux
  provider_darwin.go       // GOOS=darwin
  provider_windows.go      // GOOS=windows
  socks.go                 // go-socks5 in-process
  supervisor.go            // core rotate/pick/server list (reused from cmd/vpngate-supervisor)
```

### 2.2 Wiring

- `config.Load()` adds `VPN_ENABLED` (default true) + `VPN_PROVIDER=auto` (`auto|vpngate|direct`). Keep `VPNGATE_HOST/_CTRL_PORT` deprecated for compat.
- `server.New()` → `detectProvider(cfg)` via `runtime.GOOS` switch or build-tag files → if `exec.LookPath(openvpnBin)` fails → warn + `dialer.SetDirect(true)` fallback (existing `internal/server/server.go:134` path).
- SOCKS listens on `127.0.0.1:9050` in-process (no `VPNGATE_HOST`).
- `vpnUI` wraps `Provider` instead of `*vpngate.Controller`.

### 2.3 OS switch details

- **linux:** `startOpenVPN` unchanged (`--config`, `tun0`, `waitTunDown` via `net.InterfaceByName("tun0")`), probe `openvpn` in PATH.
- **darwin:** probe `["openvpn","/opt/homebrew/bin/openvpn","/usr/local/bin/openvpn"]`, tun devices `tun0..tun5`.
- **windows:** `LookPath("openvpn.exe")`, arg `--dev tun`, check `OpenVPNService` via `sc query`, TAP-Windows6 driver existence. Same `exec` flow.

## 3. Components

| Component | File | Responsibility | Reused from |
|-----------|------|----------------|-------------|
| Provider interface | `vpn/provider.go` | `Start`, `Rotate`, `ConnectTo`, `ListServers`, `CurrentIP`, `Status`, `Ping`, `Close` | `supervisor` struct |
| OS exec | `provider_{linux,darwin,windows}.go` | `startOpenVPN`, `killProcess`, `waitTunnelUp/Down`, `Preflight` | `supervisor:613-720` |
| SOCKS | `vpn/socks.go` | `serveSOCKS` with `go-socks5` | `supervisor:924` |
| Core logic | `vpn/supervisor.go` | `pickWeighted`, `selectionWeight`, `getServers`, `fetchServerList`, `matchCountry` | `supervisor:438-583` |
| Server wiring | `internal/server/server.go` | Provider lifecycle, `vpnUI`, `Dialer` | existing |
| Config | `internal/config/config.go` | `VPN_ENABLED`, `VPN_PROVIDER`, deprecate old vars | existing |

No changes to `upstream`, `translate`, `recorder`.

## 4. Data Flow

```
[CLI] freegate --port 1234 [--vpn=false]
  → config.Load() → detectOS() → NewProvider(auto)
  → Provider.Start(bgCtx) // reconnectLoop + ipRefresher + serveSOCKS
  → Server.Run() // chi router + upstream refreshers
  → Upstream.Dialer → SOCKS 127.0.0.1:9050 → tunX → VPNGate relay → upstreams
Dashboard: ListServers/ConnectTo → Provider directly (no HTTP hop), toggle Direct via Dialer
```

## 5. Privilege & Fallback

- `Preflight()` at startup: if `openvpn` missing → direct mode with banner `VPN unavailable: install openvpn or run with --vpn=false`.
- If `tun` permission denied → `startOpenVPN` error → fallback direct → dashboard shows `disconnected` + `Retry`.
- No auto-elevate; docs recommend `sudo freegate` (linux/mac) or `Run as Administrator` (windows). Flag `--vpn=false` bypasses all checks.

## 6. Error Handling

- `fetchServerList` 20s timeout, stale cache fallback (keeps serving).
- `Rotate` 3 attempts (`rotateAttempts`), `waitTunnelUp` 8s + `ipCheckTimeout` 6s, `404/409` for not-found/in-progress.
- Empty catalog `len==0` → keep cache (avoid wiping).
- In-process errors are Go errors (no HTTP 100s timeout).

## 7. Testing

- Unit: `vpn/provider_test.go` with `execCommand` var injection + mocked `fetchServerList`.
- OS-specific: `//go:build linux` etc for `matchCountry`, `selectionWeight` (reuse `supervisor/main_test.go`).
- CI: `go vet ./...`, `go test ./...` per OS matrix.
- Manual: `freegate --vpn=false` must pass handler tests without openvpn.

## 8. Distribution

- `CGO_ENABLED=0`, `.goreleaser.yaml` matrix: `linux/amd64,arm64`, `darwin/amd64,arm64`, `windows/amd64` → `freegate`/`freegate.exe` tar.gz/zip.
- Docker `Dockerfile` remains for `proxy`-only image; `docker-compose.yml` renamed to `docker-compose.vpn.yml` legacy.
- UX:

```bash
sudo ./freegate --port 1234          # linux/mac embedded VPN
./freegate --vpn=false --port 1234   # direct
# windows Admin PowerShell
.\freegate.exe --port 1234
```

## 9. Docs & Migration

- `README.md` Quick Start split Single Binary vs Docker.
- `docs/ENV.md` add `VPN_ENABLED`, `VPN_PROVIDER`.
- `.env.example` add new vars, deprecate old with comments.

## 10. Non-Goals

- Fully static bundle (no embedded openvpn binary/driver).
- wireguard-go replacement (VPNGate only offers OpenVPN).
- Auto sudo elevation.

## 11. Self-Review Checklist

- [x] No TBD/TODO placeholders
- [x] Sections consistent (provider interface matches supervisor reuse)
- [x] Scope is single spec (vpn per-OS), no decomposition needed
- [x] No ambiguous requirements (explicit fallback to direct)
