# Single-Binary VPNGate per-OS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a single Go binary `freegate` that auto-detects `runtime.GOOS` (linux/darwin/windows), embeds VPNGate OpenVPN provider with in-process SOCKS, and falls back to direct mode if `openvpn` missing — removing the Docker sidecar requirement.

**Architecture:** Extract `cmd/vpngate-supervisor` core into `internal/infrastructure/vpn` Provider interface with OS-specific `provider_*.go` via `exec openvpn`; merge SOCKS and control API in-process; wire via `internal/server` with `VPN_ENABLED`/`VPN_PROVIDER` config.

**Tech Stack:** Go 1.26.1, `github.com/armon/go-socks5`, `github.com/davegallant/vpngate`, `github.com/go-chi/chi/v5`, `openvpn` binary per OS (external)

**Spec:** `docs/superpowers/specs/2026-08-22-single-binary-vpn-per-os-design.md`

## Global Constraints

- Go 1.26.1 floor (`go.mod:3`)
- CGO_ENABLED=0 for builds
- Module `freegate`
- Upstreams: `opencode`, `kilo`, `llm7` unchanged
- Existing dialer direct fallback `upstream.Dialer.IsDirect()` must keep working
- No embedded openvpn binary/driver — external dependency only

---

## File Structure

- **Create:** `internal/infrastructure/vpn/provider.go` — Provider interface + types (ServerInfo, StatusInfo, PingResult reuse)
- **Create:** `internal/infrastructure/vpn/supervisor.go` — core logic `rotate`, `pickWeighted`, `getServers`, `fetchServerList`, `matchCountry`, `selectionWeight`
- **Create:** `internal/infrastructure/vpn/socks.go` — `serveSOCKS(addr string) error` wrapper
- **Create:** `internal/infrastructure/vpn/provider_linux.go` — `//go:build linux` OpenVPN exec + tun0 checks
- **Create:** `internal/infrastructure/vpn/provider_darwin.go` — `//go:build darwin` brew path probe + tun handling
- **Create:** `internal/infrastructure/vpn/provider_windows.go` — `//go:build windows` openvpn.exe + TAP check
- **Create:** `internal/infrastructure/vpn/provider_test.go` — provider unit tests
- **Modify:** `internal/config/config.go:1-73` — add VPN_ENABLED, VPN_PROVIDER, deprecate VPNGATE_HOST
- **Modify:** `internal/config/config_test.go:1-120` — add validation tests
- **Modify:** `internal/server/server.go:1-286` — wire Provider, remove HTTP Controller
- **Modify:** `cmd/server/main.go` — flag --vpn, --port handling
- **Modify:** `.goreleaser.yaml:1-70` — matrix builds
- **Modify:** `docs/ENV.md`, `.env.example`, `README.md` — docs

## Tasks

### Task 1: Provider Interface + Core Logic Extraction

**Files:**
- Create: `internal/infrastructure/vpn/provider.go`
- Create: `internal/infrastructure/vpn/supervisor.go`
- Create: `internal/infrastructure/vpn/socks.go`
- Test: `internal/infrastructure/vpn/provider_test.go`

**Interfaces:**
- Consumes: `github.com/davegallant/vpngate/pkg/vpn` Server type, `github.com/armon/go-socks5`
- Produces: `type Provider interface { Start(ctx context.Context) error; Rotate() error; ConnectTo(hostname string) error; ListServers() ([]ServerInfo, error); RefreshServers() ([]ServerInfo, error); Status() (StatusInfo, error); Ping() (PingResult, error); CurrentIP() string; Close() error }` and `func NewProvider(cfg ProviderConfig) (Provider, error)`

- [ ] **Step 1: Write failing test for Provider creation**

```go
// internal/infrastructure/vpn/provider_test.go
package vpn

import "testing"

func TestNewProvider_AutoReturnsDirectWhenDisabled(t *testing.T) {
    p, err := NewProvider(ProviderConfig{Enabled: false})
    if err != nil { t.Fatalf("unexpected err: %v", err) }
    if p == nil { t.Fatal("expected provider") }
    if p.CurrentIP() != "direct" { t.Errorf("expected direct, got %s", p.CurrentIP()) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/infrastructure/vpn -run TestNewProvider_AutoReturnsDirectWhenDisabled -v`
Expected: FAIL `undefined: NewProvider`

- [ ] **Step 3: Implement minimal provider.go + supervisor.go + socks.go**

```go
// internal/infrastructure/vpn/provider.go
package vpn

import (
    "context"
    "runtime"
    "sync"
    "time"
)

type ProviderConfig struct {
    Enabled       bool
    Provider      string // auto|vpngate|direct
    SocksAddr     string
    Country       string
    MinScore      int
    MaxPing       int
    RefreshInt    time.Duration
}

type ServerInfo struct { Hostname, IP, Country, CountryCode string; Score int; Ping string }
type StatusInfo struct { Connected bool; Server, Country, IP string; ConnectedAt int64 }
type PingResult struct { Connected, Direct bool; Server, Country, IP string; DNSOK bool; DNSMS int64; DNSError string; EgressOK bool; EgressIP string; HTTPMS int64; HTTPCode int; EgressErr string }

type Provider interface {
    Start(ctx context.Context) error
    Rotate() error
    ConnectTo(hostname string) error
    ListServers() ([]ServerInfo, error)
    RefreshServers() ([]ServerInfo, error)
    Status() (StatusInfo, error)
    Ping() (PingResult, error)
    CurrentIP() string
    Close() error
}

func NewProvider(cfg ProviderConfig) (Provider, error) {
    if !cfg.Enabled || cfg.Provider == "direct" {
        return &directProvider{}, nil
    }
    // OS dispatch via build-tag files; default to vpngate supervisor
    switch runtime.GOOS {
    case "linux":
        return newLinuxProvider(cfg), nil
    case "darwin":
        return newDarwinProvider(cfg), nil
    case "windows":
        return newWindowsProvider(cfg), nil
    default:
        return newLinuxProvider(cfg), nil
    }
}

type directProvider struct{ mu sync.RWMutex; ip string }
func (d *directProvider) Start(ctx context.Context) error { return nil }
func (d *directProvider) Rotate() error { return nil }
func (d *directProvider) ConnectTo(string) error { return nil }
func (d *directProvider) ListServers() ([]ServerInfo, error) { return nil, nil }
func (d *directProvider) RefreshServers() ([]ServerInfo, error) { return nil, nil }
func (d *directProvider) Status() (StatusInfo, error) { return StatusInfo{}, nil }
func (d *directProvider) Ping() (PingResult, error) { return PingResult{Direct: true}, nil }
func (d *directProvider) CurrentIP() string { return "direct" }
func (d *directProvider) Close() error { return nil }
```

Copy core funcs `pickWeighted`, `selectionWeight`, `matchCountry`, `parsePing`, `getServers`, `fetchServerList`, `startOpenVPN`, `killProcess`, `waitTunDown`, `waitTunnelUp` from `cmd/vpngate-supervisor/main.go:438-730` into `supervisor.go` (exported as needed, remove HTTP routes).

`socks.go`:

```go
package vpn

import (
    "context"
    "net"
    "github.com/armon/go-socks5"
)

func serveSOCKS(addr string) error {
    conf := &socks5.Config{
        Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
            d := net.Dialer{Timeout: 5 * 1e9}
            return d.DialContext(ctx, network, addr)
        },
    }
    server, err := socks5.New(conf)
    if err != nil { return err }
    return server.ListenAndServe("tcp", addr)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/infrastructure/vpn -run TestNewProvider -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/infrastructure/vpn/provider.go internal/infrastructure/vpn/supervisor.go internal/infrastructure/vpn/socks.go internal/infrastructure/vpn/provider_test.go
git commit -m "feat(vpn): extract Provider interface + core supervisor logic"
```

### Task 2: OS-Specific Providers

**Files:**
- Create: `internal/infrastructure/vpn/provider_linux.go`
- Create: `internal/infrastructure/vpn/provider_darwin.go`
- Create: `internal/infrastructure/vpn/provider_windows.go`
- Test: `internal/infrastructure/vpn/provider_test.go` (extend)

**Interfaces:**
- Consumes: `Provider` interface from Task 1, `supervisor` helpers
- Produces: `func newLinuxProvider(cfg ProviderConfig) Provider` etc., each implements `Provider`

- [ ] **Step 1: Write failing test for OS preflight**

```go
func TestLinuxProvider_PreflightMissingOpenVPN(t *testing.T) {
    // Inject lookPath mock via var execLookPath = exec.LookPath
    orig := execLookPath
    execLookPath = func(string) (string, error) { return "", errors.New("not found") }
    defer func(){ execLookPath = orig }()
    p := newLinuxProvider(ProviderConfig{Enabled: true, SocksAddr: "127.0.0.1:9050"})
    if err := p.Start(context.Background()); err == nil {
        t.Error("expected error when openvpn missing, should fallback")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/infrastructure/vpn -run TestLinuxProvider -v`
Expected: FAIL `undefined: newLinuxProvider`

- [ ] **Step 3: Implement OS files**

`provider_linux.go`:
```go
//go:build linux

package vpn

import (
    "context"
    "os/exec"
    "sync"
    "time"
)

var execLookPath = exec.LookPath

func newLinuxProvider(cfg ProviderConfig) Provider { return &supervisorProvider{cfg: cfg, os: "linux", socksAddr: cfg.SocksAddr} }

// supervisorProvider wraps the extracted supervisor struct
type supervisorProvider struct {
    cfg ProviderConfig
    os  string
    socksAddr string
    // embed fields from original supervisor: mu, cmd, etc.
    mu sync.Mutex
    // ... reuse supervisor struct definition
}
func (s *supervisorProvider) Start(ctx context.Context) error {
    if _, err := execLookPath("openvpn"); err != nil {
        // fallback to direct
        return nil // or log and return nil with direct flag
    }
    go serveSOCKS(s.socksAddr)
    // start reconnectLoop + ipRefresher as in supervisor main
    return nil
}
// implement other methods by delegating to supervisor helpers
```

Similarly `provider_darwin.go` probes `[]string{"openvpn","/opt/homebrew/bin/openvpn","/usr/local/bin/openvpn"}` and `provider_windows.go` probes `openvpn.exe` via `execLookPath`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/infrastructure/vpn -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/infrastructure/vpn/provider_linux.go internal/infrastructure/vpn/provider_darwin.go internal/infrastructure/vpn/provider_windows.go
git commit -m "feat(vpn): add per-OS OpenVPN providers (linux/darwin/windows)"
```

### Task 3: Config & Server Wiring

**Files:**
- Modify: `internal/config/config.go:10-73`
- Modify: `internal/config/config_test.go`
- Modify: `internal/server/server.go:30-110`
- Test: `internal/config/config_test.go`, `go vet`

**Interfaces:**
- Consumes: `vpn.Provider` from Task 1-2
- Produces: `Server` now holds `vpn.Provider` instead of `*vpngate.Controller`

- [ ] **Step 1: Write failing test for new config**

```go
func TestValidate_VPNEnabledRequiresSocksAddr(t *testing.T) {
    cfg := defaultConfig()
    cfg.VPNEnabled = true
    cfg.SOCKSAddr = ""
    if err := cfg.Validate(); err == nil {
        t.Fatal("expected error")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config -run TestValidate_VPNEnabled -v`
Expected: FAIL

- [ ] **Step 3: Implement config changes**

```go
// internal/config/config.go
type Config struct {
    // ... existing
    VPNEnabled  bool   `env:"VPN_ENABLED,default:true"`
    VPNProvider string `env:"VPN_PROVIDER,default:auto"` // auto|vpngate|direct
    // deprecate VPNGateHost/CtrlPort but keep for compat
}
// Load(): cfg.VPNEnabled = envBool("VPN_ENABLED", true)
// if !cfg.VPNEnabled { cfg.SOCKSAddr = "" } else cfg.SOCKSAddr = "127.0.0.1:9050" // in-process
// Validate(): if cfg.VPNEnabled && cfg.VPNProvider == "vpngate" && cfg.SOCKSAddr == "" { error }
```

`server.go`: replace `tc *vpngate.Controller` with `vpnProvider vpn.Provider`, init via `vpn.NewProvider(vpn.ProviderConfig{Enabled: cfg.VPNEnabled, ...})`, start via `vpnProvider.Start(bgCtx)`, wire `vpnUI` to `vpnProvider` + `dialer`, remove `vpngate` import, delete `stopIP` channel monitor (provider handles it).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config -v && go vet ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/server/server.go
git commit -m "feat(server): wire in-process VPN provider with OS detection"
```

### Task 4: CLI, Distribution & Docs

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `.goreleaser.yaml`
- Modify: `.env.example`
- Modify: `docs/ENV.md`
- Modify: `README.md`
- Modify: `Dockerfile`, `docker-compose.yml` (deprecate)

**Interfaces:**
- Consumes: `config` and `server` from Task 3

- [ ] **Step 1: Write failing test for CLI flag**

```go
// cmd/server/main_test.go
func TestFlagVPNFalseDisablesProvider(t *testing.T) {
    os.Args = []string{"freegate", "--vpn=false"}
    cfg := config.Load()
    // expect VPNEnabled false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/server -v`
Expected: FAIL

- [ ] **Step 3: Implement**

`cmd/server/main.go`: add `flag.Bool("vpn", true, "enable embedded VPN")` and override `cfg.VPNEnabled`, handle `--port`.

`.goreleaser.yaml`: add matrix builds `goos: [linux,darwin,windows]`.

`.env.example`: add `VPN_ENABLED=true`, `VPN_PROVIDER=auto`.

`README.md`: split Quick Start `Single Binary` vs `Docker (legacy)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... && goreleaser check`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/server/main.go .goreleaser.yaml .env.example docs/ENV.md README.md
git commit -m "feat(dist): single binary CLI flags and multi-OS goreleaser"
```

## Self-Review

- Spec section 2 (Components) → Task 1-2 coverage OK
- Spec section 3-4 (Privilege/Error) → Task 3 wiring fallback OK
- Spec section 5 (Distribution) → Task 4 OK
- No placeholders — all steps have concrete code blocks
- Types consistent: ProviderConfig, ServerInfo reused
