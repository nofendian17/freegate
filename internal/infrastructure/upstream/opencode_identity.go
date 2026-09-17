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

// OpenCode client identity for Zen free-tier validation.
//
// Mirrors 9router PR #10 (cherry-pick of decolua#4105): the gateway rejects
// anonymous free-tier requests unless they carry a current OpenCode client
// User-Agent (opencode/<version>, version >= 1.17.0) and an
// x-opencode-session in canonical descending form
// (ses_ + 12 hex timestamp digits + 14 Base62 chars).
const (
	openCodeFallbackVersion = "1.18.31"
	// openCodeClientVersionTTL bounds how often the cached client version
	// is refreshed from GitHub releases (mirrors 9router's 12h default).
	openCodeClientVersionTTL    = 12 * time.Hour
	openCodeVersionFetchTimeout = 8 * time.Second
)

// openCodeReleasesURL is a var so tests can point it at a local server.
var openCodeReleasesURL = "https://api.github.com/repos/anomalyco/opencode/releases/latest"

var openCodeClientVersion atomic.Value // stores string

// zenRequestBodyLogLimit caps the logged request body. Tools, model, and
// headers always fit; image payloads get truncated with a marker.
const zenRequestBodyLogLimit = 8192

// logZenRequest emits the exact outgoing Zen request when
// UPSTREAM_CAPTURE=true: endpoint, wire headers, and body. Same trust
// policy as response capture (full conversation content — debug only on
// trusted machines), except credentials are always redacted: only the
// well-known anonymous "public" values are shown verbatim.
func logZenRequest(endpoint string, headers map[string]string, body []byte) {
	if os.Getenv("UPSTREAM_CAPTURE") != "true" {
		return
	}
	safe := make(map[string]string, len(headers))
	for k, v := range headers {
		safe[k] = redactZenCredential(k, v)
	}
	logBody := string(body)
	if len(logBody) > zenRequestBodyLogLimit {
		logBody = logBody[:zenRequestBodyLogLimit] + "...[truncated]"
	}
	slog.Info("upstream zen request", "endpoint", endpoint, "headers", safe, "body", logBody)
}

// redactZenCredential keeps the anonymous "public" markers visible (needed
// to debug free-tier validation) while redacting real keys.
func redactZenCredential(key, value string) string {
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
	if v := strings.TrimSpace(os.Getenv("OPENCODE_CLIENT_VERSION")); v != "" {
		if norm, ok := normalizeClientVersion(v); ok && meetsMinClientVersion(norm) {
			openCodeClientVersion.Store(norm)
		}
	}
}

// openCodeUserAgent returns the User-Agent sent to the Zen gateway:
// opencode/<cached-or-pinned-version> with the provider-utils/runtime
// suffixes the genuine client sends.
func openCodeUserAgent() string {
	v, _ := openCodeClientVersion.Load().(string)
	if v == "" {
		v = openCodeFallbackVersion
	}
	return "opencode/" + v + " ai-sdk/provider-utils/4.0.46 runtime/bun/1.3.14"
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
// (opencode/1.17.x and newer).
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
	return major > translate.MinOpencodeClientMajor || (major == translate.MinOpencodeClientMajor && minor >= translate.MinOpencodeClientMinor)
}

// refreshOpenCodeClientVersion fetches the latest OpenCode release tag and
// caches it as the advertised client version. Fail-open: any fetch, parse,
// or below-minimum result keeps the current (fallback) version.
func refreshOpenCodeClientVersion(ctx context.Context) error {
	client := &http.Client{Timeout: openCodeVersionFetchTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openCodeReleasesURL, nil)
	if err != nil {
		return fmt.Errorf("opencode client version: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("opencode client version: fetch releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode client version: releases API %d", resp.StatusCode)
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("opencode client version: decode releases: %w", err)
	}
	norm, ok := normalizeClientVersion(payload.TagName)
	if !ok {
		return fmt.Errorf("opencode client version: no semver tag_name in %q", payload.TagName)
	}
	if !meetsMinClientVersion(norm) {
		return fmt.Errorf("opencode client version: %s below minimum", norm)
	}
	openCodeClientVersion.Store(norm)
	slog.Info("opencode client version refreshed", "version", norm)
	return nil
}
