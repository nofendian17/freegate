package upstream

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"time"

	anyllm "github.com/mozilla-ai/any-llm-go"
	"github.com/mozilla-ai/any-llm-go/providers"
	"github.com/mozilla-ai/any-llm-go/providers/openai"
	"golang.org/x/net/proxy"
)

const providerRequestTimeout = 0

// anyllmProvider is the upstream adapter built on top of mozilla-ai/any-llm-go.
// It wraps an anyllm.Provider (typically openai.New with a custom base URL and
// Tor-routed *http.Client) and a per-upstream free-filter callback.
type anyllmProvider struct {
	name     string
	prefixes []string
	provider providers.Provider
	cache    *ModelCache
	freePred func(providers.Model) bool
}

// newAnyLLMProvider builds an anyllmProvider that talks to baseURL through
// SOCKS5 (socksAddr) and filters ListModels results with freePred.
func newAnyLLMProvider(name, baseURL, apiKey, socksAddr string, headers map[string]string, prefixes []string, freePred func(providers.Model) bool) (*anyllmProvider, error) {
	hc := newTorClient(socksAddr, headers)
	p, err := openai.New(
		anyllm.WithAPIKey(apiKey),
		anyllm.WithBaseURL(baseURL),
		anyllm.WithHTTPClient(hc),
	)
	if err != nil {
		return nil, err
	}
	if freePred == nil {
		freePred = func(providers.Model) bool { return true }
	}
	return &anyllmProvider{
		name:     name,
		prefixes: prefixes,
		provider: p,
		cache:    NewModelCache(),
		freePred: freePred,
	}, nil
}

func (a *anyllmProvider) Name() string { return a.name }

func (a *anyllmProvider) Match(modelID string) bool {
	if len(a.prefixes) == 0 {
		// default upstream matches everything
		return true
	}
	if strings.HasSuffix(modelID, ":free") {
		return true
	}
	for _, p := range a.prefixes {
		if strings.HasPrefix(modelID, p) {
			return true
		}
	}
	return false
}

// newTorClient returns an *http.Client that dials through the SOCKS5 proxy at
// socksAddr. If socksAddr is empty, it returns a direct client.
func newTorClient(socksAddr string, headers map[string]string) *http.Client {
	hc := &http.Client{Timeout: providerRequestTimeout}
	if socksAddr != "" {
		dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
		if err == nil {
			tr := &http.Transport{ForceAttemptHTTP2: false, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
			if dc, ok := dialer.(proxy.ContextDialer); ok {
				tr.DialContext = dc.DialContext
			} else {
				tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				}
			}
			hc.Transport = tr
		}
	}
	if len(headers) > 0 {
		hc.Transport = &headerTransport{base: hc.Transport, headers: headers}
	}
	return hc
}

// headerTransport injects fixed request headers into every request.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	if t.base == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	return t.base.RoundTrip(req)
}

// Ensure the unused-time import doesn't break the build when other parts
// of the file are removed in later tasks. The `_ = time.Duration(0)` line
// is a no-op and is removed in the final cleanup of this file.
var _ = time.Duration(0)
