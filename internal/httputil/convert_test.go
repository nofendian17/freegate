package httputil

import (
	"encoding/json"
	"testing"
)

func TestInt64(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int64
	}{
		{"int", int(42), 42},
		{"int64", int64(42), 42},
		{"int32", int32(42), 42},
		{"float64", float64(42.0), 42},
		{"json.Number", json.Number("42"), 42},
		{"nil", nil, 0},
		{"string", "42", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Int64(tt.in); got != tt.want {
				t.Errorf("Int64(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
