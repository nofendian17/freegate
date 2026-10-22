package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"freegate/internal/translate"
)

// OpenCode client identity for free-tier validation.
//
// Mirrors 9router PR #4842 (fix/opencode-free-inference-v2): since
// 2026-10-14 the v1 "opencode/1.18.x" User-Agent form and the /zen/v1
// endpoint are rejected with 403 FreeTierError. Free inference moved to
// /inference/v1 and requires the v2 UA shape
// opencode/<channel>/<version>/<client> (e.g. opencode/latest/2.0.26/cli)
// with major >= 2.
const (
	openCodeFallbackVersion = "2.0.32"
	// openCodeClientVersionTTL bounds how often the cached client version
	// is refreshed from GitHub releases (mirrors 9router's 12h default).
	openCodeClientVersionTTL    = 12 * time.Hour
	openCodeVersionFetchTimeout = 8 * time.Second
)

// openCodeReleasesURL is a var so tests can point it at a local server.
var openCodeReleasesURL = "https://registry.npmjs.org/@opencode/cli/latest"

// openCodeVersionClient is reused across probes; its Timeout bounds each
// release fetch (openCodeVersionFetchTimeout).
var openCodeVersionClient = &http.Client{Timeout: openCodeVersionFetchTimeout}

var openCodeClientVersion atomic.Value // stores string

// logOpenCodeRequest emits the exact outgoing inference request when
// UPSTREAM_CAPTURE=true: endpoint, wire headers, and full body (no
// truncation — capture only on trusted machines). Same trust policy as
// response capture (full conversation content), except credentials are
// always redacted: only the well-known anonymous "public" values are
// shown verbatim.
func logOpenCodeRequest(endpoint string, headers map[string]string, body []byte) {
	if os.Getenv("UPSTREAM_CAPTURE") != "true" {
		return
	}
	safe := make(map[string]string, len(headers))
	for k, v := range headers {
		safe[k] = redactOpenCodeCredential(k, v)
	}
	slog.Info("upstream opencode request", "endpoint", endpoint, "headers", safe, "body", string(body))
}

// redactOpenCodeCredential keeps the anonymous "public" markers visible
// (needed to debug free-tier validation) while redacting real keys.
func redactOpenCodeCredential(key, value string) string {
	switch strings.ToLower(key) {
	case "authorization":
		if value == "Bearer public" {
			return value
		}
		return "Bearer <redacted>"
	case "x-api-key":
		if value == "public" {
			return value
		}
		return "<redacted>"
	default:
		return value
	}
}

func init() {
	openCodeClientVersion.Store(openCodeFallbackVersion)
	// OPENCODE_CLIENT_VERSION pins the advertised client version (e.g.
	// for air-gapped setups where the GitHub release probe can't run).
	// Values below the gateway minimum are ignored.
	if norm, ok := pinnedClientVersion(); ok {
		openCodeClientVersion.Store(norm)
	}
}

// pinnedClientVersion returns the OPENCODE_CLIENT_VERSION override when it
// is set to a valid above-minimum version. A pin also disables the
// background release probe (see clientVersionProbeEnabled), so a later
// probe success can never overwrite the operator's choice.
func pinnedClientVersion() (string, bool) {
	if v := strings.TrimSpace(os.Getenv("OPENCODE_CLIENT_VERSION")); v != "" {
		if norm, ok := normalizeClientVersion(v); ok && meetsMinClientVersion(norm) {
			return norm, true
		}
	}
	return "", false
}

// clientVersionProbeEnabled reports whether the background GitHub release
// probe should run. It is skipped when the version is pinned: no network
// call, no failure noise, and the pin can't be overwritten.
func clientVersionProbeEnabled() bool {
	_, pinned := pinnedClientVersion()
	return !pinned
}

// openCodeUserAgent returns the User-Agent sent to the inference gateway:
// opencode/latest/<cached-or-pinned-version>/cli. Upstream rejects the v1
// "opencode/1.18.x" form with 403 FreeTierError since 2026-10-14
// (9router PR #4842, captured from the v2.0.26 CLI).
func openCodeUserAgent() string {
	v, _ := openCodeClientVersion.Load().(string)
	if v == "" {
		v = openCodeFallbackVersion
	}
	return "opencode/latest/" + v + "/cli"
}

var releaseTagRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// normalizeClientVersion trims a leading "v" and keeps the leading
// major.minor.patch triple ("v1.18.31" -> "1.18.31"). ok=false when the
// value has no semver triple.
func normalizeClientVersion(tag string) (norm string, ok bool) {
	m := releaseTagRE.FindStringSubmatch(strings.TrimSpace(tag))
	if m == nil {
		return "", false
	}
	return m[1] + "." + m[2] + "." + m[3], true
}

// meetsMinClientVersion reports whether v satisfies the gateway floor
// (opencode v2 and newer; v1 UAs are rejected with 403 since 2026-10-14).
func meetsMinClientVersion(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	if major > translate.MinOpencodeClientMajor {
		return true
	}
	return major == translate.MinOpencodeClientMajor && minor >= translate.MinOpencodeClientMinor
}

// refreshOpenCodeClientVersion fetches the latest OpenCode release tag and
// caches it as the advertised client version. Fail-open: any fetch, parse,
// or below-minimum result keeps the current (fallback) version.
func refreshOpenCodeClientVersion(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openCodeReleasesURL, nil)
	if err != nil {
		return fmt.Errorf("opencode client version: build request: %w", err)
	}
	resp, err := openCodeVersionClient.Do(req)
	if err != nil {
		return fmt.Errorf("opencode client version: fetch npm: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode client version: npm registry %d", resp.StatusCode)
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("opencode client version: decode npm: %w", err)
	}
	norm, ok := normalizeClientVersion(payload.Version)
	if !ok {
		return fmt.Errorf("opencode client version: no semver version in %q", payload.Version)
	}
	if !meetsMinClientVersion(norm) {
		return fmt.Errorf("opencode client version: %s below minimum", norm)
	}
	openCodeClientVersion.Store(norm)
	slog.Info("opencode client version refreshed", "version", norm)
	return nil
}
