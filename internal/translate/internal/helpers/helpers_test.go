package helpers

import (
	"strings"
	"testing"
)

func TestSplitSSELines(t *testing.T) {
	chunks, rest := SplitSSE("data: a\ndata: b\npartial", "\n")
	if len(chunks) != 2 || chunks[0] != "data: a" || chunks[1] != "data: b" {
		t.Fatalf("chunks = %q, want [data: a data: b]", chunks)
	}
	if rest != "partial" {
		t.Fatalf("rest = %q, want partial", rest)
	}
}

func TestSplitSSEBlocks(t *testing.T) {
	chunks, rest := SplitSSE("data: a\n\ndata: b\n\ntail", "\n\n")
	if len(chunks) != 2 || chunks[0] != "data: a" || chunks[1] != "data: b" {
		t.Fatalf("chunks = %q, want [data: a data: b]", chunks)
	}
	if rest != "tail" {
		t.Fatalf("rest = %q, want tail", rest)
	}
}

func TestSplitSSEEmpty(t *testing.T) {
	chunks, rest := SplitSSE("", "\n")
	if len(chunks) != 0 || rest != "" {
		t.Fatalf("chunks = %q, rest = %q; want empty", chunks, rest)
	}
}

func TestAsInt64(t *testing.T) {
	for _, tt := range []struct {
		in   any
		want int64
		ok   bool
	}{
		{float64(42), 42, true},
		{5, 5, true},
		{int64(7), 7, true},
		{"x", 0, false},
		{nil, 0, false},
	} {
		got, ok := AsInt64(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("AsInt64(%v) = (%d,%v), want (%d,%v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestAsInt(t *testing.T) {
	if got := AsInt(float64(3)); got != 3 {
		t.Errorf("AsInt(3.0) = %d, want 3", got)
	}
	if got := AsInt("x"); got != 0 {
		t.Errorf("AsInt(x) = %d, want 0", got)
	}
}

func TestRandomID(t *testing.T) {
	id := RandomID(12)
	if len(id) != 12 {
		t.Fatalf("len = %d, want 12", len(id))
	}
	if strings.Trim(id, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		t.Fatalf("id %q has out-of-alphabet chars", id)
	}
	if RandomID(12) == id {
		t.Error("two RandomID calls returned the same value")
	}
}
