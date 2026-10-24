package upstream

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestOpenCodeLiveBunTLS is an opt-in integration test: it hits the real
// OpenCode free tier with an anonymous key and a minimal chat body. The
// upstream gate requires the Bun ClientHello, the read/shell tool
// fingerprints, and an OpenCode-style <env> block; ChatCompletion injects
// the latter two. Run with:
//
//	OPENCODE_LIVE_TEST=1 go test ./internal/infrastructure/upstream -run TestOpenCodeLiveBunTLS
func TestOpenCodeLiveBunTLS(t *testing.T) {
	if os.Getenv("OPENCODE_LIVE_TEST") != "1" {
		t.Skip("set OPENCODE_LIVE_TEST=1 to hit the live OpenCode gateway")
	}
	body := []byte(`{"model":"deepseek-v4.1-flash-free","messages":[{"role":"user","content":"Reply with OK"}],"stream":true}`)
	for _, base := range []string{
		"https://opencode.ai/inference/v1",
		"https://opencode.ai/inference/openai/v1",
	} {
		t.Run(base, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			u := NewOpenCodeUpstreamWithTransport(base, []string{"public"}, nil, nil)
			resp, err := u.ChatCompletion(ctx, body)
			if err != nil {
				t.Fatalf("chat: %v", err)
			}
			defer func() { _ = resp.Close() }()
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
			}
			if !strings.Contains(string(raw), "deepseek-v4.1-flash-free") {
				t.Fatalf("unexpected body: %s", raw)
			}
		})
	}
}
