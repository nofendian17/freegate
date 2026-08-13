package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostSetsAcceptHeaderForStream(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept")
		w.WriteHeader(200)
	}))
	defer srv.Close()

client := NewHTTPClient(srv.URL, []string{"key"}, "", nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

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

client := NewHTTPClient(srv.URL, []string{"key"}, "", nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

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

client := NewHTTPClient(srv.URL, []string{"key"}, "", nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"foo"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

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

	client := NewHTTPClient(srv.URL, []string{"key-a", "key-b"}, "", nil)
	for i := 0; i < 4; i++ {
		resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
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

	client := NewHTTPClient(srv.URL, []string{"key-a"}, "", nil)
	for i := 0; i < 3; i++ {
		resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
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

	client := NewHTTPClient(srv.URL, []string{"key-a", "key-b"}, "", nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

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

	client := NewHTTPClient(srv.URL, []string{"key-a", "key-b"}, "", nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

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

	client := NewHTTPClient(srv.URL, []string{"key-a", "key-b"}, "", nil)
	for i := 0; i < 3; i++ {
		resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
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

	client := NewHTTPClient(srv.URL, []string{"key-a"}, "", nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}
	if len(got) != 1 {
		t.Fatalf("expected a single attempt with one key, got %d (%v)", len(got), got)
	}
}
