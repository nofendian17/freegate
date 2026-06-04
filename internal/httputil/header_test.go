package httputil

import (
	"net/http"
	"testing"
)

func TestCopyHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Set("X-Custom", "value")

	dst := http.Header{}
	CopyHeaders(dst, src)

	if got := dst.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := dst.Get("X-Custom"); got != "value" {
		t.Errorf("X-Custom = %q, want value", got)
	}
}

func TestCopyHeadersSkipsHopByHop(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Set("Connection", "close")
	src.Set("Keep-Alive", "timeout=5")

	dst := http.Header{}
	CopyHeaders(dst, src)

	if dst.Get("Connection") != "" {
		t.Errorf("Connection should be skipped, got %q", dst.Get("Connection"))
	}
	if dst.Get("Keep-Alive") != "" {
		t.Errorf("Keep-Alive should be skipped, got %q", dst.Get("Keep-Alive"))
	}
	if dst.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type should be copied, got %q", dst.Get("Content-Type"))
	}
}

func TestCopyHeadersPreservesMultiValue(t *testing.T) {
	src := http.Header{}
	src.Add("X-Multi", "one")
	src.Add("X-Multi", "two")
	src.Add("X-Multi", "three")

	dst := http.Header{}
	CopyHeaders(dst, src)

	got := dst.Values("X-Multi")
	want := []string{"one", "two", "three"}
	if len(got) != len(want) {
		t.Fatalf("X-Multi has %d values, want %d: %v", len(got), len(want), got)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("X-Multi[%d] = %q, want %q", i, got[i], v)
		}
	}
}
