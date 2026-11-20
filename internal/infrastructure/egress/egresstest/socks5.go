// Package egresstest provides a minimal forwarding SOCKS5 server for tests.
// It speaks just enough of RFC 1928 to let egress pools dial through it, and
// forwards bytes both ways so an HTTP request can complete.
package egresstest

import (
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// Socks5 is a running SOCKS5 listener.
type Socks5 struct {
	ln     net.Listener
	target atomic.Value // string
	wg     sync.WaitGroup
}

// New starts a SOCKS5 server on loopback and stops it when the test ends.
func New(t *testing.T) *Socks5 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &Socks5{ln: ln}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		s.wg.Wait()
	})
	return s
}

// Addr returns the listener address.
func (s *Socks5) Addr() string { return s.ln.Addr().String() }

// Target returns the last requested destination.
func (s *Socks5) Target() string {
	v, _ := s.target.Load().(string)
	return v
}

func (s *Socks5) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	var greeting [3]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return
	}
	var host string
	switch head[3] {
	case 0x01:
		var b [4]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return
		}
		host = net.IP(b[:]).String()
	case 0x04:
		var b [16]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return
		}
		host = net.IP(b[:]).String()
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(conn, b); err != nil {
			return
		}
		host = string(b)
	default:
		return
	}
	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(port[0])<<8|int(port[1])))
	s.target.Store(target)

	upstream, err := net.Dial("tcp", target)
	if err != nil {
		// SOCKS5 reply: version 5, REP=1 (general failure), RSV, ATYP=IPv4.
		_, _ = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer func() { _ = upstream.Close() }()
	// Success with an all-zero IPv4 bound address.
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, conn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, upstream)
		done <- struct{}{}
	}()
	<-done
	<-done
}
