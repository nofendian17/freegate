//go:build egresslive

// Live check against a real WARP sidecar. It is excluded from the normal test
// run because it needs a container, a working WARP tunnel, and Cloudflare
// connectivity. Build the image with `make up-warp` (or `docker compose -f
// docker-compose.yml -f docker-compose.warp.yml build warp-1`) and run one
// sidecar with its ports published:
//
//	docker run --rm --name warp-live \
//	  -p 127.0.0.1:1080:1080 -p 127.0.0.1:8081:8081 \
//	  --cap-add NET_ADMIN --cap-add SYS_MODULE --device /dev/net/tun \
//	  freegate-warp:latest
//
//	EGRESS_TEST_SOCKS=127.0.0.1:1080 \
//	EGRESS_TEST_CONTROL=http://127.0.0.1:8081 \
//	go test -tags egresslive ./internal/infrastructure/egress -run Live -v
package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveRotation(t *testing.T) {
	socks := os.Getenv("EGRESS_TEST_SOCKS")
	control := os.Getenv("EGRESS_TEST_CONTROL")
	if socks == "" || control == "" {
		t.Skip("set EGRESS_TEST_SOCKS and EGRESS_TEST_CONTROL to run the live check")
	}

	p := NewPool(Options{
		Endpoints:      []Endpoint{{Name: "live", Socks: socks, Control: control}},
		FallbackDirect: false,
		RotateTimeout:  90 * time.Second,
	})

	ctx, lease := p.Acquire(context.Background())
	first := traceIP(t, p, ctx)
	if first == "" {
		t.Fatal("no exit IP through the sidecar")
	}
	t.Logf("exit before rotation: %s", first)

	<-lease.RotateAsync("live test", nil)
	waitRotated(t, p, 1)

	second := traceIP(t, p, context.Background())
	if second == "" {
		t.Fatal("no exit IP after rotation")
	}
	t.Logf("exit after rotation: %s", second)
	if first == second {
		t.Fatalf("exit IP did not change: %s", first)
	}
}

// traceIP reads Cloudflare's /cdn-cgi/trace over plain HTTP through the pool.
// HTTP keeps the check independent of TLS and of which host name the sidecar
// can resolve.
func traceIP(t *testing.T, p *Pool, parent context.Context) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()

	raw, err := p.DialContext(ctx, "tcp", "[2606:4700:4700::1111]:443")
	if err != nil {
		t.Fatalf("dial through pool: %v", err)
	}
	defer func() { _ = raw.Close() }()
	_ = raw.SetDeadline(time.Now().Add(30 * time.Second))

	conn := tls.Client(raw, &tls.Config{ServerName: "cloudflare-dns.com", MinVersion: tls.VersionTLS12})
	if err := conn.HandshakeContext(ctx); err != nil {
		t.Fatalf("tls handshake through pool: %v", err)
	}
	req := "GET /cdn-cgi/trace HTTP/1.1\r\nHost: cloudflare-dns.com\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatalf("write trace request: %v", err)
	}
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read trace response: %v", err)
	}
	br := bufio.NewReader(strings.NewReader(string(body)))
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ip="); ok {
			return v
		}
	}
	t.Fatalf("no ip= line in trace response:\n%s", truncate(string(body), 400))
	return ""
}

func waitRotated(t *testing.T, p *Pool, want int) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		snap := p.Snapshot()[0]
		if snap.Rotations >= want && snap.Status == StatusReady {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("rotation never completed: %+v", p.Snapshot()[0])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
