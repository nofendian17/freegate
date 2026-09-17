package domain

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestIsFreeTierRejection(t *testing.T) {
	rejection := `{"type":"error","error":{"type":"FreeTierError","message":"Error from provider (Console): OpenCode's free tier can only be used from within OpenCode"}}`
	cases := []struct {
		name string
		resp *UpstreamResponse
		want bool
	}{
		{"nil response", nil, false},
		{"nil body", &UpstreamResponse{StatusCode: 403}, false},
		{"free tier error", &UpstreamResponse{StatusCode: 403, Body: io.NopCloser(strings.NewReader(rejection)), Header: http.Header{}}, true},
		{"other error", &UpstreamResponse{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"bad request"}}`)), Header: http.Header{}}, false},
		{"empty body", &UpstreamResponse{StatusCode: 403, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, false},
	}
	for _, tc := range cases {
		if got := IsFreeTierRejection(tc.resp); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsFreeTierRejection_TopLevelType(t *testing.T) {
	resp := &UpstreamResponse{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"type":"FreeTierError","message":"nope"}`)), Header: http.Header{}}
	if !IsFreeTierRejection(resp) {
		t.Fatal("expected top-level type detected")
	}
}

func TestIsFreeTierRejection_PreservesLargeBody(t *testing.T) {
	pad := strings.Repeat("x", freeTierProbeLimit*2)
	body := `{"error":{"type":"FreeTierError","message":"nope"}}` + pad
	resp := &UpstreamResponse{StatusCode: 403, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
	if !IsFreeTierRejection(resp) {
		t.Fatal("expected rejection detected")
	}
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read after probe: %v", err)
	}
	if string(rest) != body {
		t.Fatalf("body truncated: %d of %d bytes", len(rest), len(body))
	}
	if err := resp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestIsFreeTierRejection_RestoresBody(t *testing.T) {
	body := `{"type":"error","error":{"type":"FreeTierError","message":"nope"}}`
	resp := &UpstreamResponse{StatusCode: 403, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
	if !IsFreeTierRejection(resp) {
		t.Fatal("expected rejection detected")
	}
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read after probe: %v", err)
	}
	if string(rest) != body {
		t.Fatalf("body not restored, got %q", rest)
	}
}
