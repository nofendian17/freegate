package upstream

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	anyllm "github.com/mozilla-ai/any-llm-go"
	"github.com/mozilla-ai/any-llm-go/providers"
	"github.com/mozilla-ai/any-llm-go/providers/openai"
	"golang.org/x/net/proxy"

	"freegate/internal/model"
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

// NewAnyLLMProvider builds an Upstream that talks to baseURL through SOCKS5
// (socksAddr) and filters ListModels results with freePred. If freePred is
// nil, every model is kept.
func NewAnyLLMProvider(name, baseURL, apiKey, socksAddr string, headers map[string]string, prefixes []string, freePred func(providers.Model) bool) (Upstream, error) {
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

// ListModels calls the upstream's /models endpoint, applies freePred, and
// returns the matching free models.
func (a *anyllmProvider) ListModels(ctx context.Context) ([]model.Model, error) {
	lister, ok := a.provider.(providers.ModelLister)
	if !ok {
		return nil, fmt.Errorf("%s: provider does not support ListModels", a.name)
	}
	resp, err := lister.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: list models: %w", a.name, err)
	}
	out := make([]model.Model, 0, len(resp.Data))
	for _, m := range resp.Data {
		if !a.freePred(m) {
			continue
		}
		out = append(out, model.Model{
			ID:       m.ID,
			Object:   m.Object,
			Created:  m.Created,
			OwnedBy:  m.OwnedBy,
			IsFree:   true,
			Provider: a.name,
		})
	}
	return out, nil
}

// Models returns the cached free models. Returns nil if no refresh has run yet.
func (a *anyllmProvider) Models() []model.Model { return a.cache.Get() }

// Start kicks off the periodic model-refresh loop using the shared Refresher.
func (a *anyllmProvider) Start(ctx context.Context, refreshInterval time.Duration) {
	r := NewRefresher(a.name, func(ctx context.Context) error {
		models, err := a.ListModels(ctx)
		if err != nil {
			return err
		}
		a.cache.Set(models)
		return nil
	}, refreshInterval)
	r.Start(ctx)
}

// Provider returns the underlying any-llm-go Provider so the proxy can call
// Completion / CompletionStream directly.
func (a *anyllmProvider) Provider() anyllm.Provider { return a.provider }

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
