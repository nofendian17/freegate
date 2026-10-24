package upstream

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
)

// OpenCode's free-tier gate fingerprints the TLS ClientHello, not just the
// HTTP request. The official CLI runs on Bun, and its ClientHello is accepted
// while Go's crypto/tls handshake is rejected with 429 FreeUsageLimitError
// even when every HTTP header, body byte, path, and IP is identical.
//
// The spec below is captured from Bun 1.4.4 (the version shipped with the
// current OpenCode CLI) and reproduced with uTLS. Its JA3 is
// 53cccb85beeda42c810fd4701e4737f5; keep opencode_tls_test.go green when
// changing it. The flag exists so a future upstream change can fall back to
// stock Go TLS without a code change.
const openCodeBunTLSEnv = "UPSTREAM_OPENCODE_BUN_TLS"

// openCodeBunTLSEnabled defaults to true: the free tier is unusable from Go
// without the Bun-shaped handshake. Set UPSTREAM_OPENCODE_BUN_TLS=false to
// disable the compatibility layer.
func openCodeBunTLSEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(openCodeBunTLSEnv))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// newOpenCodeHTTPClient builds the OpenCode client with a dedicated transport
// that presents Bun's ClientHello to the OpenCode host. Relays and any other
// hosts reached by this transport keep standard Go TLS, so relay behavior is
// unchanged.
func newOpenCodeHTTPClient(baseURL string, apiKeys []string, headers map[string]string, tr *http.Transport) *HTTPClient {
	if openCodeBunTLSEnabled() {
		tr = newOpenCodeBunTransport(baseURL, tr)
	}
	return NewHTTPClientWithTransport(baseURL, apiKeys, headers, tr)
}

func newOpenCodeBunTransport(baseURL string, base *http.Transport) *http.Transport {
	if base == nil {
		base = NewTransport()
	}
	tr := base.Clone()
	// Bun speaks HTTP/1.1 and advertises only http/1.1 in ALPN. Keeping h2
	// enabled would both change the wire format and negotiate a protocol the
	// captured ClientHello never offers.
	tr.ForceAttemptHTTP2 = false
	tr.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	tr.DialTLSContext = openCodeBunDialTLS(openCodeTargetHost(baseURL), tr)
	return tr
}

// openCodeTargetHost returns the host whose TLS handshake must look like Bun.
// Relays rewrite req.URL to their own host, so they are intentionally excluded.
func openCodeTargetHost(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func openCodeBunDialTLS(targetHost string, tr *http.Transport) func(context.Context, string, string) (net.Conn, error) {
	dial := tr.DialContext
	if dial == nil {
		d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		dial = d.DialContext
	}
	timeout := tr.TLSHandshakeTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		raw, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		hctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if targetHost == "" || !strings.EqualFold(host, targetHost) {
			return handshakeOpenCodeStandardTLS(hctx, raw, host, tr)
		}
		return handshakeOpenCodeBunTLS(hctx, raw, host, tr)
	}
}

func handshakeOpenCodeStandardTLS(ctx context.Context, raw net.Conn, host string, tr *http.Transport) (net.Conn, error) {
	cfg := &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}
	if tr.TLSClientConfig != nil {
		cfg = tr.TLSClientConfig.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName = host
		}
		// HTTP/2 is disabled for this transport; never negotiate it.
		cfg.NextProtos = []string{"http/1.1"}
	}
	conn := tls.Client(raw, cfg)
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}

func handshakeOpenCodeBunTLS(ctx context.Context, raw net.Conn, host string, tr *http.Transport) (net.Conn, error) {
	cfg := &utls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}
	if tr.TLSClientConfig != nil {
		cfg.InsecureSkipVerify = tr.TLSClientConfig.InsecureSkipVerify
		cfg.RootCAs = tr.TLSClientConfig.RootCAs
		if tr.TLSClientConfig.ServerName != "" {
			cfg.ServerName = tr.TLSClientConfig.ServerName
		}
		if tr.TLSClientConfig.MinVersion != 0 {
			cfg.MinVersion = tr.TLSClientConfig.MinVersion
		}
		if tr.TLSClientConfig.MaxVersion != 0 {
			cfg.MaxVersion = tr.TLSClientConfig.MaxVersion
		}
	}
	u := utls.UClient(raw, cfg, utls.HelloCustom)
	if err := u.ApplyPreset(openCodeBunClientHelloSpec()); err != nil {
		_ = raw.Close()
		return nil, err
	}
	if err := u.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return u, nil
}

// openCodeBunClientHelloSpec is Bun 1.4.4's TLS ClientHello as observed on the
// wire. Field order matters: JA3 hashes the extension and cipher order, and
// OpenCode's gate appears to allowlist this exact shape. Changes to this
// function are fingerprint changes, not cosmetic refactors.
func openCodeBunClientHelloSpec() *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		TLSVersMin: utls.VersionTLS12,
		TLSVersMax: utls.VersionTLS13,
		CipherSuites: []uint16{
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
		CompressionMethods: []uint8{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},
			&utls.ExtendedMasterSecretExtension{},
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateNever},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{
				utls.X25519MLKEM768,
				utls.X25519,
				utls.CurveP256,
				utls.CurveP384,
			}},
			&utls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&utls.SessionTicketExtension{},
			&utls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
			&utls.StatusRequestExtension{},
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256,
				utls.PSSWithSHA256,
				utls.PKCS1WithSHA256,
				utls.ECDSAWithP384AndSHA384,
				utls.PSSWithSHA384,
				utls.PKCS1WithSHA384,
				utls.PSSWithSHA512,
				utls.PKCS1WithSHA512,
				utls.Ed25519,
				utls.ECDSAWithP521AndSHA512,
				utls.PKCS1WithSHA1,
			}},
			&utls.SCTExtension{},
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{
				{Group: utls.X25519MLKEM768},
				{Group: utls.X25519},
			}},
			&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
			&utls.SupportedVersionsExtension{Versions: []uint16{utls.VersionTLS13, utls.VersionTLS12}},
		},
	}
}
