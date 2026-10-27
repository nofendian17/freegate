package upstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNewTransport_AttemptsHTTP2(t *testing.T) {
	// Regression guard: h2 multiplexes concurrent streams over one tunnel
	// TCP+TLS connection, avoiding lossy-tunnel handshakes. A custom
	// DialContext disables h2 unless ForceAttemptHTTP2 is set.
	tr := NewTransport()
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2=false disables h2 with the custom dialer")
	}
}

func TestPostSetsAcceptHeaderForStream(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"key"}, nil, nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !strings.Contains(got, "text/event-stream") {
		t.Errorf("expected Accept: text/event-stream for stream=true, got %q", got)
	}
}

func TestPostOmitsAcceptForNonStreaming(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"key"}, nil, nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got == "text/event-stream" {
		t.Errorf("expected no Accept: text/event-stream for stream=false, got %q", got)
	}
}

func TestPostOmitsAcceptWhenStreamFieldMissing(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"key"}, nil, nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"foo"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got == "text/event-stream" {
		t.Errorf("expected no Accept: text/event-stream when stream field missing, got %q", got)
	}
}

func TestPostRotatesApiKeyAcrossRequests(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"key-a", "key-b"}, nil, nil)
	for i := 0; i < 4; i++ {
		resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	want := []string{"Bearer key-a", "Bearer key-b", "Bearer key-a", "Bearer key-b"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("request %d: expected %q, got %q", i, w, got[i])
		}
	}
}

func TestPostUsesSingleKeyWhenOneConfigured(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"key-a"}, nil, nil)
	for i := 0; i < 3; i++ {
		resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	for i, g := range got {
		if g != "Bearer key-a" {
			t.Errorf("request %d: expected Bearer key-a, got %q", i, g)
		}
	}
}

func TestPostRetriesWithNextKeyOn429(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		got = append(got, auth)
		if auth == "Bearer key-a" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"key-a", "key-b"}, nil, nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after failover, got %d", resp.StatusCode)
	}
	want := []string{"Bearer key-a", "Bearer key-b"}
	if len(got) != len(want) {
		t.Fatalf("expected %d attempts, got %d (%v)", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("attempt %d: expected %q, got %q", i, w, got[i])
		}
	}
}

func TestPostReturns429WhenAllKeysLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"key-a", "key-b"}, nil, nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}
}

func TestPostSkipsLimitedKeyOnSubsequentRequests(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		got = append(got, auth)
		if auth == "Bearer key-a" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"key-a", "key-b"}, nil, nil)
	for i := 0; i < 3; i++ {
		resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, resp.StatusCode)
		}
	}

	// First request: a fails (429), b succeeds. Key-a then goes into cooldown,
	// so the next two requests go straight to key-b.
	want := []string{"Bearer key-a", "Bearer key-b", "Bearer key-b", "Bearer key-b"}
	if len(got) != len(want) {
		t.Fatalf("expected %d attempts, got %d (%v)", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("attempt %d: expected %q, got %q", i, w, got[i])
		}
	}
}

func TestPostDoesNotRetry429WithSingleKey(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"key-a"}, nil, nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}
	if len(got) != 1 {
		t.Fatalf("expected a single attempt with one key, got %d (%v)", len(got), got)
	}
}

func TestPostWithHeaders_SyncsXApiKeyWithBearer(t *testing.T) {
	type pair struct{ auth, apiKey string }
	var got []pair
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, pair{r.Header.Get("Authorization"), r.Header.Get("x-api-key")})
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"custom-key"}, nil, nil)
	resp, err := client.PostWithHeaders(context.Background(), "/chat", []byte(`{"model":"x"}`),
		map[string]string{"x-api-key": "public"})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if len(got) != 1 {
		t.Fatalf("expected 1 request, got %d", len(got))
	}
	if got[0].auth != "Bearer custom-key" {
		t.Errorf("expected Bearer custom-key, got %q", got[0].auth)
	}
	if got[0].apiKey != "custom-key" {
		t.Errorf("expected x-api-key synced to custom-key, got %q", got[0].apiKey)
	}
}

func TestPostWithHeaders_PreservesExplicitXApiKey(t *testing.T) {
	var apiKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey = r.Header.Get("x-api-key")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"custom-key"}, nil, nil)
	resp, err := client.PostWithHeaders(context.Background(), "/chat", []byte(`{"model":"x"}`),
		map[string]string{"x-api-key": "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if apiKey != "explicit" {
		t.Errorf("expected explicit x-api-key preserved, got %q", apiKey)
	}
}

func TestPostWithHeaders_StripsPublicXApiKey(t *testing.T) {
	// The genuine OpenCode client authenticates anonymous requests with
	// `Authorization: Bearer public` only and never sends x-api-key; the
	// public marker is a non-genuine fingerprint the free-tier gate
	// rejects with 403 FreeTierError.
	type pair struct {
		auth, apiKey string
		hasApiKey    bool
	}
	var got []pair
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, has := r.Header["X-Api-Key"]
		got = append(got, pair{r.Header.Get("Authorization"), r.Header.Get("x-api-key"), has})
		w.WriteHeader(200)
	}))
	defer srv.Close()

	for _, keys := range [][]string{{"public"}, nil} {
		client := NewHTTPClientWithTransport(srv.URL, keys, nil, nil)
		resp, err := client.PostWithHeaders(context.Background(), "/chat", []byte(`{"model":"x"}`),
			map[string]string{"x-api-key": "public", "Content-Type": "application/json"})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(got))
	}
	for i, p := range got {
		if p.hasApiKey {
			t.Errorf("request %d: x-api-key = %q, want absent", i, p.apiKey)
		}
		if p.auth != "Bearer public" && p.auth != "Bearer" {
			t.Errorf("request %d: Authorization = %q", i, p.auth)
		}
	}
}

func TestPostWithHeaders_PreservesExplicitXApiKeyWithPublicBearer(t *testing.T) {
	var apiKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey = r.Header.Get("x-api-key")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"public"}, nil, nil)
	resp, err := client.PostWithHeaders(context.Background(), "/chat", []byte(`{"model":"x"}`),
		map[string]string{"x-api-key": "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if apiKey != "explicit" {
		t.Errorf("expected explicit non-public x-api-key preserved, got %q", apiKey)
	}
}

// TestReadAllKeepsGzipOnHealthyCatalog guards the cheap path: a healthy
// upstream must be fetched with the transport's own gzip negotiation, not
// identity. Kilo's catalog is ~543 KB raw versus ~68 KB gzipped and refreshes
// every minute, so forcing identity on the happy path costs bandwidth for
// nothing.
func TestReadAllKeepsGzipOnHealthyCatalog(t *testing.T) {
	var gotEncoding string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Accept-Encoding")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, nil, nil, nil)
	if _, err := client.ReadAll(context.Background(), "/models"); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if gotEncoding != "gzip" {
		t.Errorf("Accept-Encoding=%q, want gzip on the healthy path", gotEncoding)
	}
}

// TestReadAllRetriesMislabeledBodyAsIdentity covers the relay failure mode: a
// hop decodes the upstream body with its own fetch() but forwards
// Content-Encoding: gzip, so the transport's transparent decoder fails with
// "gzip: invalid header" and loses every byte (oven-sh/bun#35546). ReadAll must
// retry that decode-level failure with Accept-Encoding: identity, where a
// stale header is inert.
func TestReadAllRetriesMislabeledBodyAsIdentity(t *testing.T) {
	var mu sync.Mutex
	var encodings []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		encodings = append(encodings, r.Header.Get("Accept-Encoding"))
		mu.Unlock()
		// Decoded body with a stale encoding label: the relay failure mode.
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"big-pickle"}]}`))
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, nil, nil, nil)
	body, err := client.ReadAll(context.Background(), "/models")
	if err != nil {
		t.Fatalf("ReadAll with stale Content-Encoding: %v", err)
	}
	if !strings.Contains(string(body), "big-pickle") {
		t.Errorf("unexpected body: %q", body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(encodings) != 2 {
		t.Fatalf("requests=%d %v, want a gzip attempt then an identity retry", len(encodings), encodings)
	}
	if encodings[0] != "gzip" || encodings[1] != "identity" {
		t.Errorf("encodings=%v, want [gzip identity]", encodings)
	}
}

// TestReadAllInflatesCompressedBody keeps the identity path honest: an upstream
// that compresses anyway is decoded from the bytes' magic number, not from the
// header.
func TestReadAllInflatesCompressedBody(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(`{"object":"list","data":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, nil, nil, nil)
	// Identity request: the transport does not decode, so the magic-byte
	// path in readCatalog is what has to inflate it.
	body, err := client.readCatalog(context.Background(), "/models",
		map[string]string{"Accept-Encoding": "identity"})
	if err != nil {
		t.Fatalf("readCatalog with gzipped body: %v", err)
	}
	if string(body) != `{"object":"list","data":[]}` {
		t.Errorf("unexpected body: %q", body)
	}
}

// TestReadAllReportsEmptyBody guards against the opaque failure seen on a
// custom upstream: an empty 2xx reached json.Unmarshal and surfaced only as
// "parse models: unexpected end of JSON input", losing the status and
// content-type that explain it. ReadAll retries once (a relay can answer empty
// on the first attempt), then reports what the upstream said.
func TestReadAllReportsEmptyBody(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"key"}, nil, nil)
	_, err := client.ReadAll(context.Background(), "/models")
	if err == nil {
		t.Fatal("expected an error for an empty 200 body")
	}
	for _, want := range []string{"empty body", "200", "application/json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Errorf("requests=%d, want one retry before giving up", requests)
	}
}

// TestReadAllReportsStatusAndSnippet keeps a rejecting upstream from hiding
// behind a JSON parse error: the caller must see the status and the body, and a
// 401 must not be retried — a second request cannot change the answer.
func TestReadAllReportsStatusAndSnippet(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("{\"error\":\n  \"invalid api key\"}"))
	}))
	defer srv.Close()

	client := NewHTTPClientWithTransport(srv.URL, []string{"key"}, nil, nil)
	_, err := client.ReadAll(context.Background(), "/models")
	if err == nil {
		t.Fatal("expected an error for a 401 catalog response")
	}
	for _, want := range []string{"401", "invalid api key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error snippet must be single-line, got %q", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Errorf("requests=%d, want no retry for an HTTP status failure", requests)
	}
}
