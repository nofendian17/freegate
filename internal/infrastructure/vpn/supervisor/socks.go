package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/armon/go-socks5"
)

// serveSOCKS runs the SOCKS5 proxy that freegate's upstream client dials
// through the tunnel. The dial timeout bounds how long a client connection
// waits when the tunnel is down (fail fast instead of hanging); it does
// not bound the lifetime of established streams.
func serveSOCKS(addr string) error {
	conf := &socks5.Config{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, addr)
		},
	}
	server, err := socks5.New(conf)
	if err != nil {
		return fmt.Errorf("create socks5 server: %w", err)
	}
	slog.Info("vpngate: socks5 listening", "addr", addr)
	return server.ListenAndServe("tcp", addr)
}
