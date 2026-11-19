package egress

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	socksVersion = 0x05
	socksNoAuth  = 0x00

	socksCmdConnect = 0x01

	socksAtypIPv4   = 0x01
	socksAtypDomain = 0x03
	socksAtypIPv6   = 0x04
)

// dialSOCKS5 opens a TCP connection to targetAddr through the SOCKS5 proxy at
// proxyAddr. Only the no-authentication method is supported, which is what the
// WARP sidecars expose.
//
// The target host is sent as a domain name whenever it is not an IP literal, so
// the proxy (inside the WARP tunnel) resolves it and the caller never leaks a
// DNS query outside the tunnel.
func dialSOCKS5(ctx context.Context, d *net.Dialer, proxyAddr, targetAddr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		return nil, fmt.Errorf("socks5: invalid target %q: %w", targetAddr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("socks5: invalid port in %q", targetAddr)
	}

	conn, err := d.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("socks5: dial proxy %s: %w", proxyAddr, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()

	if deadline, has := ctx.Deadline(); has {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(d.Timeout))
	}

	// Greeting: version 5, one method, "no authentication".
	if _, err := conn.Write([]byte{socksVersion, 0x01, socksNoAuth}); err != nil {
		return nil, fmt.Errorf("socks5: greeting: %w", err)
	}
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return nil, fmt.Errorf("socks5: greeting reply: %w", err)
	}
	if greeting[0] != socksVersion {
		return nil, fmt.Errorf("socks5: unexpected version %d", greeting[0])
	}
	if greeting[1] != socksNoAuth {
		return nil, fmt.Errorf("socks5: proxy requires authentication (method %#x)", greeting[1])
	}

	req := []byte{socksVersion, socksCmdConnect, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = append(req, socksAtypIPv4)
			req = append(req, ip4...)
		} else {
			req = append(req, socksAtypIPv6)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("socks5: hostname too long (%d bytes)", len(host))
		}
		req = append(req, socksAtypDomain, byte(len(host)))
		req = append(req, host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))

	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("socks5: connect request: %w", err)
	}

	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return nil, fmt.Errorf("socks5: connect reply: %w", err)
	}
	if head[0] != socksVersion {
		return nil, fmt.Errorf("socks5: unexpected reply version %d", head[0])
	}
	if head[1] != 0x00 {
		return nil, fmt.Errorf("socks5: connect rejected: %s", socksReplyText(head[1]))
	}
	if err := discardSocksAddr(conn, head[3]); err != nil {
		return nil, err
	}

	_ = conn.SetDeadline(time.Time{})
	ok = true
	return conn, nil
}

// discardSocksAddr consumes the bound address in a SOCKS5 reply so the stream
// is positioned at the first application byte.
func discardSocksAddr(r io.Reader, atyp byte) error {
	var n int
	switch atyp {
	case socksAtypIPv4:
		n = net.IPv4len
	case socksAtypIPv6:
		n = net.IPv6len
	case socksAtypDomain:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return fmt.Errorf("socks5: bound address: %w", err)
		}
		n = int(l[0])
	default:
		return fmt.Errorf("socks5: unknown address type %#x", atyp)
	}
	// Address plus the 2-byte port.
	buf := make([]byte, n+2)
	if _, err := io.ReadFull(r, buf); err != nil {
		return fmt.Errorf("socks5: bound address: %w", err)
	}
	return nil
}

func socksReplyText(code byte) string {
	switch code {
	case 0x01:
		return "general SOCKS server failure"
	case 0x02:
		return "connection not allowed by ruleset"
	case 0x03:
		return "network unreachable"
	case 0x04:
		return "host unreachable"
	case 0x05:
		return "connection refused"
	case 0x06:
		return "TTL expired"
	case 0x07:
		return "command not supported"
	case 0x08:
		return "address type not supported"
	default:
		return fmt.Sprintf("unknown error %#x", code)
	}
}

// decodeRotation reads the optional {"ip": "..."} body returned by the control
// API. A body that is absent or not JSON is not an error: the rotation itself
// succeeded and the IP is only informational.
func decodeRotation(r io.Reader) string {
	if r == nil {
		return ""
	}
	var out struct {
		IP string `json:"ip"`
	}
	body, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return ""
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return ""
	}
	return out.IP
}
