package prepost

import (
	"testing"
)

func TestEnsureStreamOptions(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "adds stream_options when stream is true",
			in:   `{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			want: `{"messages":[{"content":"hi","role":"user"}],"model":"gpt-4","stream":true,"stream_options":{"include_usage":true}}`,
		},
		{
			name: "noop when stream is false",
			in:   `{"model":"gpt-4","stream":false,"messages":[{"role":"user","content":"hi"}]}`,
			want: `{"model":"gpt-4","stream":false,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "noop when stream is missing",
			in:   `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
			want: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "noop when stream_options already present",
			in:   `{"model":"gpt-4","stream":true,"stream_options":{"include_usage":false},"messages":[{"role":"user","content":"hi"}]}`,
			want: `{"model":"gpt-4","stream":true,"stream_options":{"include_usage":false},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "noop on empty body",
			in:   ``,
			want: ``,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EnsureStreamOptions([]byte(tt.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("EnsureStreamOptions()\ngot:  %s\nwant: %s", string(got), tt.want)
			}
		})
	}
}

func TestEnsureStreamOptions_InvalidJSON(t *testing.T) {
	_, err := EnsureStreamOptions([]byte(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}
