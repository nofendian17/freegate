package upstream

import (
	"crypto/md5"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

func TestOpenCodeBunClientHelloJA3(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	type captured struct {
		record []byte
		err    error
	}
	ch := make(chan captured, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			ch <- captured{err: err}
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		hdr := make([]byte, 5)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			ch <- captured{err: err}
			return
		}
		if hdr[0] != 0x16 {
			ch <- captured{err: fmt.Errorf("record type = %#x, want handshake", hdr[0])}
			return
		}
		body := make([]byte, binary.BigEndian.Uint16(hdr[3:5]))
		if _, err := io.ReadFull(conn, body); err != nil {
			ch <- captured{err: err}
			return
		}
		ch <- captured{record: append(hdr, body...)}
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	cfg := &utls.Config{ServerName: "opencode.ai", InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}}
	u := utls.UClient(raw, cfg, utls.HelloCustom)
	if err := u.ApplyPreset(openCodeBunClientHelloSpec()); err != nil {
		t.Fatalf("apply preset: %v", err)
	}
	_ = u.SetDeadline(time.Now().Add(3 * time.Second))
	_ = u.Handshake() // the listener closes after capturing ClientHello

	got := <-ch
	if got.err != nil {
		t.Fatalf("capture: %v", got.err)
	}
	const want = "53cccb85beeda42c810fd4701e4737f5"
	if ja3 := clientHelloJA3(t, got.record); ja3 != want {
		t.Fatalf("JA3 = %s, want %s (Bun 1.4.4 fingerprint)", ja3, want)
	}
}

func TestOpenCodeBunTransportConfig(t *testing.T) {
	base := NewTransport()
	tr := newOpenCodeBunTransport("https://opencode.ai/inference/v1", base)

	if tr == base {
		t.Fatal("expected a cloned transport, got the shared pointer")
	}
	if tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 must be false: Bun speaks HTTP/1.1")
	}
	if tr.DialTLSContext == nil {
		t.Error("DialTLSContext must be set")
	}
	if tr.TLSNextProto == nil || len(tr.TLSNextProto) != 0 {
		t.Errorf("TLSNextProto = %v, want a non-nil empty map", tr.TLSNextProto)
	}
	if !base.ForceAttemptHTTP2 {
		t.Error("shared transport was mutated")
	}
	if base.DialTLSContext != nil {
		t.Error("shared transport was mutated")
	}
}

func TestOpenCodeBunTransport_TargetHostHandshake(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	base := NewTransport()
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	tr := newOpenCodeBunTransport(srv.URL, base)
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET through Bun transport: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

func TestOpenCodeBunTransport_RelayHostUsesStandardTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "relay")
	}))
	defer srv.Close()

	base := NewTransport()
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	// The configured OpenCode host is opencode.ai, but the request goes to the
	// test relay host, so the uTLS path must not apply.
	tr := newOpenCodeBunTransport("https://opencode.ai/inference/v1", base)
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET through standard TLS: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestOpenCodeTargetHost(t *testing.T) {
	cases := map[string]string{
		"https://opencode.ai/inference/v1": "opencode.ai",
		"https://opencode.ai:8443/x":       "opencode.ai",
		"http://127.0.0.1:1234/v1":         "127.0.0.1",
		"":                                 "",
	}
	for in, want := range cases {
		if got := openCodeTargetHost(in); got != want {
			t.Errorf("openCodeTargetHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpenCodeBunTLSDisabled(t *testing.T) {
	t.Setenv(openCodeBunTLSEnv, "false")
	base := NewTransport()
	c := newOpenCodeHTTPClient("https://opencode.ai/inference/v1", []string{"public"}, nil, base)
	if c.client.Transport != base {
		t.Fatal("disabled flag must keep the shared transport")
	}
}

func TestOpenCodeBunTLSEnabledByDefault(t *testing.T) {
	t.Setenv(openCodeBunTLSEnv, "")
	base := NewTransport()
	c := newOpenCodeHTTPClient("https://opencode.ai/inference/v1", []string{"public"}, nil, base)
	tr, ok := c.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", c.client.Transport)
	}
	if tr == base || tr.DialTLSContext == nil {
		t.Fatal("default must install the Bun TLS transport")
	}
}

// clientHelloJA3 computes JA3 from a captured TLS handshake record. The
// parser is intentionally strict: a malformed or reordered ClientHello should
// fail the test rather than silently produce a different hash.
func clientHelloJA3(t *testing.T, record []byte) string {
	t.Helper()
	if len(record) < 5 || record[0] != 0x16 {
		t.Fatalf("not a TLS handshake record: %x", record)
	}
	hs := record[5:]
	if len(hs) < 4 || hs[0] != 1 {
		t.Fatalf("not a ClientHello: %x", hs)
	}
	hsLen := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
	if hsLen+4 > len(hs) {
		t.Fatalf("truncated ClientHello: len=%d declared=%d", len(hs), hsLen+4)
	}
	p := 4
	if len(hs) < p+2+32+1 {
		t.Fatal("truncated ClientHello header")
	}
	version := binary.BigEndian.Uint16(hs[p:])
	p += 2 + 32
	sidLen := int(hs[p])
	p++
	if len(hs) < p+sidLen+2 {
		t.Fatal("truncated session id")
	}
	p += sidLen
	csLen := int(binary.BigEndian.Uint16(hs[p:]))
	p += 2
	if csLen%2 != 0 || len(hs) < p+csLen {
		t.Fatalf("invalid cipher suites length %d", csLen)
	}
	var ciphers []string
	for i := 0; i < csLen; i += 2 {
		ciphers = append(ciphers, fmt.Sprintf("%04x", binary.BigEndian.Uint16(hs[p+i:])))
	}
	p += csLen
	if len(hs) < p+1 {
		t.Fatal("truncated compression methods")
	}
	compLen := int(hs[p])
	p++
	if len(hs) < p+compLen+2 {
		t.Fatal("truncated compression methods")
	}
	p += compLen
	extLen := int(binary.BigEndian.Uint16(hs[p:]))
	p += 2
	if len(hs) < p+extLen {
		t.Fatalf("truncated extensions: len=%d declared=%d", len(hs)-p, extLen)
	}
	end := p + extLen
	var exts, curves, points []string
	for p < end {
		if end-p < 4 {
			t.Fatal("truncated extension header")
		}
		typ := binary.BigEndian.Uint16(hs[p:])
		p += 2
		ln := int(binary.BigEndian.Uint16(hs[p:]))
		p += 2
		if end-p < ln {
			t.Fatalf("truncated extension %#x", typ)
		}
		data := hs[p : p+ln]
		p += ln
		exts = append(exts, fmt.Sprintf("%04x", typ))
		switch typ {
		case 10: // supported_groups
			if len(data) < 2 {
				t.Fatal("truncated supported_groups")
			}
			n := int(binary.BigEndian.Uint16(data))
			if n%2 != 0 || len(data) < 2+n {
				t.Fatalf("invalid supported_groups length %d", n)
			}
			for i := 2; i < 2+n; i += 2 {
				curves = append(curves, fmt.Sprintf("%04x", binary.BigEndian.Uint16(data[i:])))
			}
		case 11: // ec_point_formats
			if len(data) < 1 {
				t.Fatal("truncated ec_point_formats")
			}
			n := int(data[0])
			if len(data) < 1+n {
				t.Fatalf("invalid ec_point_formats length %d", n)
			}
			for _, b := range data[1 : 1+n] {
				points = append(points, fmt.Sprintf("%02x", b))
			}
		}
	}
	if p != end {
		t.Fatalf("extension parser stopped at %d, want %d", p, end)
	}
	ja3 := fmt.Sprintf("%d,%s,%s,%s,%s",
		version,
		strings.Join(ciphers, "-"),
		strings.Join(exts, "-"),
		strings.Join(curves, "-"),
		strings.Join(points, "-"),
	)
	sum := md5.Sum([]byte(ja3))
	return hex.EncodeToString(sum[:])
}
