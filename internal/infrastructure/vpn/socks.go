package vpn

import (
	"context"
	"net"

	"github.com/armon/go-socks5"
)

func serveSOCKS(addr string) error {
	conf := &socks5.Config{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * 1e9}
			return d.DialContext(ctx, network, addr)
		},
	}
	server, err := socks5.New(conf)
	if err != nil {
		return err
	}
	return server.ListenAndServe("tcp", addr)
}
