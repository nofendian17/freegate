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

	client := NewHTTPClient(srv.URL, "key", "", nil)
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

	client := NewHTTPClient(srv.URL, "key", "", nil)
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

	client := NewHTTPClient(srv.URL, "key", "", nil)
	resp, err := client.Post(context.Background(), "/chat", []byte(`{"model":"foo"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got == "text/event-stream" {
		t.Errorf("expected no Accept: text/event-stream when stream field missing, got %q", got)
	}
}
