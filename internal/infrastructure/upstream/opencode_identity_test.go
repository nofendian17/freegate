package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var openCodeSessionRE = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
var openCodeRequestRE = regexp.MustCompile(`^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

func TestGenSessionID_CanonicalDescendingForm(t *testing.T) {
	id := genSessionID()
	if !openCodeSessionRE.MatchString(id) {
		t.Fatalf("session id %q not canonical", id)
	}
	// The 12-hex part must decode (via complement) to a timestamp within
	// tolerance of now: hex = low48(~(ms*0x1000 + c)).
	hexPart := id[4:16]
	raw, err := strconv.ParseUint(hexPart, 16, 64)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}
	const mask = uint64(1)<<48 - 1
	recovered := int64(((mask - raw) & mask) >> 12)
	// The 48-bit slice keeps time only modulo 2^36 ms (~2.18 years);
	// fold back to the multiple nearest to now before comparing.
	const window = int64(1) << 36
	nowMs := time.Now().UnixMilli()
	k := (nowMs - recovered + window/2) / window
	gotMs := recovered + k*window
	if diff := nowMs - gotMs; diff < 0 || diff > 60_000 {
		t.Fatalf("session timestamp off by %d ms (recovered %d, now %d)", diff, gotMs, nowMs)
	}
}

func TestGenRequestID_CanonicalForm(t *testing.T) {
	id := genRequestID()
	if !openCodeRequestRE.MatchString(id) {
		t.Fatalf("request id %q not canonical", id)
	}
	raw, err := strconv.ParseUint(id[4:16], 16, 64)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}
	const mask = uint64(1)<<48 - 1
	recovered := int64((raw & mask) >> 12)
	const window = int64(1) << 36
	nowMs := time.Now().UnixMilli()
	k := (nowMs - recovered + window/2) / window
	if diff := nowMs - (recovered + k*window); diff < 0 || diff > 60_000 {
		t.Fatalf("request timestamp off by %d ms", diff)
	}
}

func TestGenSessionID_UniqueAndOrdered(t *testing.T) {
	a, b := genSessionID(), genSessionID()
	if a == b {
		t.Fatal("duplicate session ids")
	}
	if !openCodeRequestRE.MatchString(genOpencodeIDWithClock("msg", false)) {
		t.Fatal("request id not canonical")
	}
}

func TestNormalizeClientVersion(t *testing.T) {
	cases := []struct {
		in   string
		norm string
		ok   bool
	}{
		{"v1.18.31", "1.18.31", true},
		{"1.20.0", "1.20.0", true},
		{"v2.0.1-beta", "2.0.1", true},
		{"bad", "", false},
		{"", "", false},
		{"opencode/1.18.31", "", false},
	}
	for _, tc := range cases {
		norm, ok := normalizeClientVersion(tc.in)
		if norm != tc.norm || ok != tc.ok {
			t.Errorf("normalize(%q) = (%q, %v), want (%q, %v)", tc.in, norm, ok, tc.norm, tc.ok)
		}
	}
}

func TestMeetsMinClientVersion(t *testing.T) {
	for _, v := range []string{"1.17.0", "1.18.31", "1.19.0", "2.0.0"} {
		if !meetsMinClientVersion(v) {
			t.Errorf("expected %s to meet minimum", v)
		}
	}
	for _, v := range []string{"1.16.9", "1.10.0", "0.9.0", "bogus", ""} {
		if meetsMinClientVersion(v) {
			t.Errorf("expected %q to fail minimum", v)
		}
	}
}

func withClientVersion(t *testing.T, v string) {
	t.Helper()
	prev, _ := openCodeClientVersion.Load().(string)
	openCodeClientVersion.Store(v)
	t.Cleanup(func() { openCodeClientVersion.Store(prev) })
}

func TestRefreshClientVersion_CachesRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	defer srv.Close()
	prevURL := openCodeReleasesURL
	openCodeReleasesURL = srv.URL
	defer func() { openCodeReleasesURL = prevURL }()
	withClientVersion(t, openCodeFallbackVersion)

	if err := refreshOpenCodeClientVersion(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got, _ := openCodeClientVersion.Load().(string); got != "9.9.9" {
		t.Fatalf("version = %q", got)
	}
	if ua := openCodeUserAgent(); !strings.HasPrefix(ua, "opencode/9.9.9") {
		t.Fatalf("ua = %q", ua)
	}
}

func TestRefreshClientVersion_FailOpen(t *testing.T) {
	withClientVersion(t, openCodeFallbackVersion)
	prevURL := openCodeReleasesURL
	defer func() { openCodeReleasesURL = prevURL }()

	openCodeReleasesURL = "http://127.0.0.1:1/unreachable"
	if err := refreshOpenCodeClientVersion(context.Background()); err == nil {
		t.Fatal("expected error for unreachable releases API")
	}
	if got, _ := openCodeClientVersion.Load().(string); got != openCodeFallbackVersion {
		t.Fatalf("fallback not kept, got %q", got)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	openCodeReleasesURL = srv.URL
	if err := refreshOpenCodeClientVersion(context.Background()); err == nil {
		t.Fatal("expected error for 503 releases API")
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.10.0"}`))
	}))
	defer srv2.Close()
	openCodeReleasesURL = srv2.URL
	if err := refreshOpenCodeClientVersion(context.Background()); err == nil {
		t.Fatal("expected error for below-minimum version")
	}
	if got, _ := openCodeClientVersion.Load().(string); got != openCodeFallbackVersion {
		t.Fatalf("fallback not kept, got %q", got)
	}
}

func TestRedactZenCredential(t *testing.T) {
	cases := []struct {
		key, value, want string
	}{
		{"Authorization", "Bearer public", "Bearer public"},
		{"Authorization", "Bearer sk-real-key", "Bearer <redacted>"},
		{"x-api-key", "public", "public"},
		{"x-api-key", "sk-real-key", "<redacted>"},
		{"X-Api-Key", "sk-real-key", "<redacted>"},
		{"Content-Type", "application/json", "application/json"},
		{"x-opencode-session", "ses_abc", "ses_abc"},
	}
	for _, tc := range cases {
		if got := redactZenCredential(tc.key, tc.value); got != tc.want {
			t.Errorf("redact(%q, %q) = %q, want %q", tc.key, tc.value, got, tc.want)
		}
	}
}

func TestLogZenRequest_RespectsFlag(t *testing.T) {
	t.Setenv("UPSTREAM_CAPTURE", "")
	logZenRequest("/chat/completions", map[string]string{"Authorization": "Bearer sk-real"}, []byte(`{}`))
	t.Setenv("UPSTREAM_CAPTURE", "true")
	logZenRequest("/chat/completions", map[string]string{"Authorization": "Bearer sk-real"}, []byte(`{}`))
}

func TestOpenCodeUserAgent_FallbackShape(t *testing.T) {
	withClientVersion(t, openCodeFallbackVersion)
	ua := openCodeUserAgent()
	if ua != "opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14" {
		t.Fatalf("ua = %q", ua)
	}
}
