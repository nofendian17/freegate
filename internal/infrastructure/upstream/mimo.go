package upstream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
	"freegate/internal/infrastructure/upstream/types"
	"freegate/internal/model"
)

const (
	mimoSessionPrefix  = "ses_"
	mimoSessionLen     = 24
	mimoSource         = "mimocode-cli-free"
	mimoMarker         = "You are MiMoCode, an interactive CLI tool that helps users with software engineering tasks."
	mimoJWTBuffer      = 5 * time.Minute
	mimoSessionChars   = "abcdefghijklmnopqrstuvwxyz0123456789"
	mimoDefaultJWTExp  = 50 * time.Minute
	mimoMaxRespBodyLen = 512
)

// Anti-abuse gate: upstream rejects requests without a Chrome-like User-Agent with 403 "Illegal access"
var mimoUserAgents = []string{
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
}

type mimoJWT struct {
	Token     string
	ExpiresAt time.Time
}

type MimoFreeUpstream struct {
	client        *http.Client
	cache         *ModelCache
	fingerprint   string
	sessionID     string
	jwt           mimoJWT
	mu            sync.Mutex
	chatURL       string
	bootstrapURL  string
}

func NewMimoFreeUpstream(chatURL, socksAddr string) *MimoFreeUpstream {
	bootstrapURL := deriveMimoBootstrapURL(chatURL)
	transport := &http.Transport{ForceAttemptHTTP2: false}
	if socksAddr != "" {
		dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
		if err != nil {
			slog.Warn("mimo-free: SOCKS5 dialer failed, using direct", "error", err)
		} else {
			if dc, ok := dialer.(proxy.ContextDialer); ok {
				transport.DialContext = dc.DialContext
			} else {
				transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				}
			}
		}
	}
	return &MimoFreeUpstream{
		client:        &http.Client{Transport: transport},
		cache:         NewModelCache(),
		fingerprint:   generateMimoFingerprint(),
		sessionID:     generateMimoSessionID(),
		chatURL:       chatURL,
		bootstrapURL:  bootstrapURL,
	}
}

func generateMimoFingerprint() string {
	hostname, _ := os.Hostname()
	cpu := "unknown"
	if runtime.GOOS == "linux" {
		if d, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(d), "\n") {
				if strings.HasPrefix(line, "model name") {
					if parts := strings.SplitN(line, ":", 2); len(parts) == 2 {
						cpu = strings.TrimSpace(parts[1])
					}
					break
				}
			}
		}
	}
	seed := fmt.Sprintf("%s|%s|%s|%s|%s",
		hostname, runtime.GOOS, runtime.GOARCH, cpu, currentUsername())
	h := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%x", h)
}

func currentUsername() string {
	u, err := os.UserHomeDir()
	if err != nil {
		return "unknown"
	}
	parts := strings.Split(u, "/")
	return parts[len(parts)-1]
}

func deriveMimoBootstrapURL(chatURL string) string {
	chatURL = strings.TrimRight(chatURL, "/")
	suffix := "/openai/chat"
	if strings.HasSuffix(chatURL, suffix) {
		return strings.TrimSuffix(chatURL, suffix) + "/bootstrap"
	}
	return chatURL + "/bootstrap"
}

func generateMimoSessionID() string {
	id := mimoSessionPrefix
	for i := 0; i < mimoSessionLen; i++ {
		id += string(mimoSessionChars[rand.Intn(len(mimoSessionChars))])
	}
	return id
}

func (m *MimoFreeUpstream) Name() string {
	return "mimo-free"
}

func (m *MimoFreeUpstream) Start(ctx context.Context, refreshInterval time.Duration) {
	refresher := NewRefresher("mimo-free", func(ctx context.Context) error {
		models, err := m.ListModels(ctx)
		if err != nil {
			return err
		}
		m.cache.Set(models)
		return nil
	}, refreshInterval)
	refresher.Start(ctx)
}

func (m *MimoFreeUpstream) Match(modelID string) bool {
	return modelID == "mimo-auto"
}

func (m *MimoFreeUpstream) ListModels(_ context.Context) ([]model.Model, error) {
	return []model.Model{
		{ID: "mimo-auto", Object: "model", OwnedBy: "mimo-free", IsFree: true, Provider: "mimo-free"},
	}, nil
}

func (m *MimoFreeUpstream) Models() []model.Model {
	return m.cache.Get()
}

func (m *MimoFreeUpstream) ChatCompletion(ctx context.Context, body []byte) (*http.Response, error) {
	modified, err := injectMimoSystemMarker(body)
	if err != nil {
		return nil, fmt.Errorf("mimo-free: inject marker: %w", err)
	}
	modified, err = forceMimoModel(modified)
	if err != nil {
		return nil, fmt.Errorf("mimo-free: force model: %w", err)
	}

	jwt, err := m.bootstrapJWT(ctx)
	if err != nil {
		return nil, fmt.Errorf("mimo-free: jwt bootstrap: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", m.chatURL, bytes.NewReader(modified))
	if err != nil {
		return nil, fmt.Errorf("mimo-free: build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-Mimo-Source", mimoSource)
	req.Header.Set("User-Agent", mimoUserAgents[rand.Intn(len(mimoUserAgents))])
	req.Header.Set("x-session-affinity", m.sessionID)

	req.Header.Set("Accept", "text/event-stream, application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mimo-free: request: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		slog.Debug("mimo-free: auth failed, re-bootstrapping JWT")
		m.mu.Lock()
		m.jwt = mimoJWT{}
		m.mu.Unlock()
		jwt, err = m.bootstrapJWT(ctx)
		if err != nil {
			return nil, fmt.Errorf("mimo-free: re-bootstrap: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+jwt)
		retryResp, err := m.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("mimo-free: retry request: %w", err)
		}
		return retryResp, nil
	}

	return resp, nil
}

func (m *MimoFreeUpstream) bootstrapJWT(ctx context.Context) (string, error) {
	m.mu.Lock()
	if m.jwt.Token != "" && time.Now().Before(m.jwt.ExpiresAt.Add(-mimoJWTBuffer)) {
		token := m.jwt.Token
		m.mu.Unlock()
		return token, nil
	}
	m.mu.Unlock()

	payload := map[string]string{"client": m.fingerprint}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", m.bootstrapURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build bootstrap request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", mimoUserAgents[rand.Intn(len(mimoUserAgents))])

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("bootstrap request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, mimoMaxRespBodyLen))
		return "", fmt.Errorf("bootstrap returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result types.MimoBootstrapResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parse bootstrap response: %w", err)
	}
	if result.JWT == "" {
		return "", fmt.Errorf("bootstrap returned empty JWT")
	}

	exp := parseMimoJWTExp(result.JWT)

	m.mu.Lock()
	m.jwt = mimoJWT{Token: result.JWT, ExpiresAt: exp}
	m.mu.Unlock()

	return result.JWT, nil
}

func parseMimoJWTExp(jwt string) time.Time {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return time.Now().Add(mimoDefaultJWTExp)
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.RawStdEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Now().Add(mimoDefaultJWTExp)
		}
	}

	var claims struct {
		Exp *int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == nil {
		return time.Now().Add(mimoDefaultJWTExp)
	}

	return time.Unix(*claims.Exp, 0)
}

func forceMimoModel(body []byte) ([]byte, error) {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return body, nil
	}
	data["model"] = "mimo-auto"
	out, err := json.Marshal(data)
	if err != nil {
		return body, nil
	}
	return out, nil
}

func injectMimoSystemMarker(body []byte) ([]byte, error) {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return body, nil
	}

	msgsRaw, ok := data["messages"]
	if !ok {
		return body, nil
	}

	msgs, ok := msgsRaw.([]any)
	if !ok {
		return body, nil
	}

	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		if role == "system" && strings.Contains(content, mimoMarker) {
			return body, nil
		}
	}

	newMsg := map[string]any{
		"role":    "system",
		"content": mimoMarker,
	}
	data["messages"] = append([]any{newMsg}, msgs...)

	out, err := json.Marshal(data)
	if err != nil {
		return body, nil
	}
	return out, nil
}
