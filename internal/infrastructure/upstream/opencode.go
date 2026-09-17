package upstream

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"freegate/internal/domain"
	"freegate/internal/infrastructure/upstream/types"
)

// OpenCode Zen User-Agent from 9router PR #4111:
// feat(opencode): route union-alpha through Messages API with compliant Zen headers.
const openCodeUA = "opencode/1.18.31 ai-sdk/provider-utils/4.0.46 runtime/bun/1.3.14"

// Models served by /zen/v1/messages (Anthropic Messages API).
// Union Alpha is a Claude-format model on the Zen gateway.
// These are defaults; use SetResponseModels/SetMessageModels to override
// from RESPONSE_MODELS / MESSAGE_MODELS config so handler and upstream agree.
var openCodeMessagesModels = map[string]bool{
	"union-alpha": true,
}

var defaultOpenCodeResponseModels = []string{"muse-spark", "muse_spark"}

var defaultOpenCodeMessageModels = []string{"union-alpha"}

type OpenCodeUpstream struct {
	client    *HTTPClient
	cache     *ModelCache
	allowlist map[string]bool
	// Substring lists (lowercased) for endpoint routing. Kept in sync with
	// handler.SetResponseModels/SetMessageModels via server wiring.
	responseModels []string
	messageModels  []string
}

func NewOpenCodeUpstream(baseURL string, apiKeys []string, d *Dialer, freeAllowlist []string) *OpenCodeUpstream {
	return NewOpenCodeUpstreamWithTransport(baseURL, apiKeys, NewTransport(d), freeAllowlist)
}

func NewOpenCodeUpstreamWithTransport(baseURL string, apiKeys []string, tr *http.Transport, freeAllowlist []string) *OpenCodeUpstream {
	// Static headers for catalog fetches (/models). Chat requests build
	// compliant per-request Zen headers in ChatCompletion (see buildOpencodeHeaders),
	// mirroring 9router's OpenCodeExecutor.buildHeaders: fresh session/request
	// IDs per call, 40-hex project ID, cli client tag, and anthropic-version
	// for /messages models.
	headers := map[string]string{
		"x-opencode-client": "cli",
		"User-Agent":        openCodeUA,
		"x-api-key":         "public",
	}
	al := make(map[string]bool, len(freeAllowlist))
	for _, id := range freeAllowlist {
		id = strings.TrimSpace(id)
		if id != "" {
			al[id] = true
		}
	}
	return &OpenCodeUpstream{
		client:         NewHTTPClientWithTransport(baseURL, apiKeys, headers, tr),
		cache:          NewModelCache(),
		allowlist:      al,
		responseModels: append([]string(nil), defaultOpenCodeResponseModels...),
		messageModels:  append([]string(nil), defaultOpenCodeMessageModels...),
	}
}

// SetResponseModels overrides the substring list routing to /responses.
// Empty input is ignored (keeps current/defaults).
func (o *OpenCodeUpstream) SetResponseModels(models []string) {
	if norm := normalizeModelPatterns(models); len(norm) > 0 {
		o.responseModels = norm
	}
}

// SetMessageModels overrides the substring list routing to /messages.
// Empty input is ignored (keeps current/defaults).
func (o *OpenCodeUpstream) SetMessageModels(models []string) {
	if norm := normalizeModelPatterns(models); len(norm) > 0 {
		o.messageModels = norm
	}
}

func normalizeModelPatterns(models []string) []string {
	var out []string
	for _, m := range models {
		m = strings.TrimSpace(strings.ToLower(m))
		if m != "" {
			out = append(out, m)
		}
	}
	return out
}

func (o *OpenCodeUpstream) Name() string {
	return "opencode"
}

func (o *OpenCodeUpstream) Start(ctx context.Context, refreshInterval time.Duration) {
	refresher := NewRefresher("opencode", func(ctx context.Context) error {
		models, err := o.ListModels(ctx)
		if err != nil {
			return err
		}
		o.cache.Set(models)
		return nil
	}, refreshInterval)
	refresher.Run(ctx)
}

func (o *OpenCodeUpstream) Match(modelID string) bool {
	return true
}

func (o *OpenCodeUpstream) ListModels(ctx context.Context) ([]domain.Model, error) {
	body, err := o.client.ReadAll(ctx, "/models")
	if err != nil {
		return nil, fmt.Errorf("opencode: fetch models: %w", err)
	}

	var list types.OpenCodeModelList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("opencode: parse models: %w", err)
	}

	// The upstream /v1/models endpoint is OpenAI-compatible and does not
	// include cost data. Free models are identified by the "-free" suffix,
	// which is the same naming convention opencode uses in its own catalog
	// (e.g. glm-4.7-free, kimi-k2.5-free, deepseek-v4-flash-free), with a
	// small allowlist for known exceptions that don't follow that
	// convention (e.g. big-pickle, which is served as deepseek-v4-flash
	// with cost 0 by the upstream).
	var free []domain.Model
	seen := make(map[string]bool)
	for _, m := range list.Data {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if strings.HasSuffix(m.ID, "-free") || o.allowlist[m.ID] {
			free = append(free, domain.Model{
				ID:       m.ID,
				Object:   m.Object,
				Created:  m.Created,
				OwnedBy:  m.OwnedBy,
				IsFree:   true,
				Provider: "opencode",
			})
		}
	}

	return free, nil
}

func (o *OpenCodeUpstream) Models() []domain.Model {
	return o.cache.Get()
}

func (o *OpenCodeUpstream) ChatCompletion(ctx context.Context, body []byte) (*domain.UpstreamResponse, error) {
	model := extractOpencodeModel(body)
	endpoint := o.buildURL(model, body)
	out := body
	if o.isMessagesModel(model) {
		out = ensureMessagesMaxTokens(body)
	}
	headers := buildOpencodeHeaders(endpoint, out)
	// x-api-key/Authorization sync is handled per attempt inside
	// HTTPClient.doWithHeaders: when Authorization carries a non-public key
	// but x-api-key is still the default "public", the client mirrors the
	// bearer key into x-api-key so 429 failover never sends a mismatched pair.
	resp, err := o.client.PostWithHeaders(ctx, endpoint, out, headers)
	if err != nil {
		return nil, err
	}
	return domain.NewUpstreamResponse(resp), nil
}

func (o *OpenCodeUpstream) buildURL(model string, body []byte) string {
	if o.isResponsesModel(model) {
		return "/responses"
	}
	if o.isMessagesModel(model) {
		return "/messages"
	}
	// Fallback to body-shape detection for responses payloads that carry
	// no (or an unlisted) model, e.g. raw /v1/responses passthrough.
	if isResponsesBody(body) {
		return "/responses"
	}
	return "/chat/completions"
}

// baseModelID strips the thinking suffix "model(level)" so registry lookups
// hit the base id. Mirrors 9router's baseModelId.
func baseModelID(model string) string {
	m := strings.TrimSpace(model)
	if idx := strings.LastIndex(m, "("); idx >= 0 && strings.HasSuffix(m, ")") {
		m = strings.TrimSpace(m[:idx])
	}
	return m
}

func (o *OpenCodeUpstream) isMessagesModel(model string) bool {
	patterns := o.messageModels
	if len(patterns) == 0 {
		patterns = defaultOpenCodeMessageModels
	}
	return matchModelSubstring(model, patterns)
}

func (o *OpenCodeUpstream) isResponsesModel(model string) bool {
	patterns := o.responseModels
	if len(patterns) == 0 {
		patterns = defaultOpenCodeResponseModels
	}
	return matchModelSubstring(model, patterns)
}

// matchModelSubstring reports whether the base model ID (thinking suffix
// stripped, lowercased) contains any of the configured substrings. This is
// the single routing semantic shared with handler.targetFormatForModel.
func matchModelSubstring(model string, patterns []string) bool {
	base := strings.ToLower(baseModelID(model))
	if base == "" {
		return false
	}
	for _, pat := range patterns {
		if pat != "" && strings.Contains(base, pat) {
			return true
		}
	}
	return false
}

func isMessagesModel(model string) bool {
	// Backward-compat wrapper using defaults (exact legacy set was
	// {"union-alpha"}; substring match on the same default preserves it
	// while also covering variants like "union-alpha-2").
	if matchModelSubstring(model, defaultOpenCodeMessageModels) {
		return true
	}
	base := strings.ToLower(baseModelID(model))
	return openCodeMessagesModels[base]
}

func isResponsesModelID(model string) bool {
	return matchModelSubstring(model, defaultOpenCodeResponseModels)
}

func extractOpencodeModel(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	return probe.Model
}

// ensureMessagesMaxTokens defaults max_tokens to 4096 for Messages API
// models when neither max_tokens nor max_output_tokens is set. Mirrors
// 9router: Anthropic rejects Messages requests without a max token budget.
func ensureMessagesMaxTokens(body []byte) []byte {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	if _, ok := raw["max_tokens"]; ok {
		return body
	}
	if _, ok := raw["max_output_tokens"]; ok {
		return body
	}
	raw["max_tokens"] = 4096
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}

// buildOpencodeHeaders returns compliant per-request Zen headers per 9router
// PR #4111: Bearer public + x-api-key public, first-party UA, cli client
// tag, timestamp-encoded session/request IDs, 40-hex project ID, streaming
// Accept, and anthropic-version for /messages.
func buildOpencodeHeaders(endpoint string, body []byte) map[string]string {
	stream := isStreamBody(body)
	isMessages := strings.HasSuffix(endpoint, "/messages")
	accept := "*/*"
	if stream {
		accept = "text/event-stream"
	}
	headers := map[string]string{
		"Content-Type":       "application/json",
		"x-api-key":          "public",
		"User-Agent":         openCodeUA,
		"x-opencode-client":  "cli",
		"x-opencode-session": genOpencodeID("ses"),
		"x-opencode-request": genOpencodeID("msg"),
		"x-opencode-project": genProjectID(),
		"Accept":             accept,
	}
	if isMessages {
		headers["anthropic-version"] = "2023-06-01"
	}
	return headers
}

func isStreamBody(body []byte) bool {
	var probe struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Stream != nil && *probe.Stream
}

func isResponsesBody(body []byte) bool {
	// Fast check: responses bodies contain top-level "input" and no "messages"
	if len(body) == 0 {
		return false
	}
	hasInput := bytes.Contains(body, []byte(`"input"`))
	if !hasInput {
		return false
	}
	// If it has "input" but no "messages", treat as responses
	hasMessages := bytes.Contains(body, []byte(`"messages"`))
	if hasInput && !hasMessages {
		return true
	}
	// More precise JSON check for edge cases where both appear
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	if _, ok := raw["input"]; ok {
		if _, hasMsg := raw["messages"]; !hasMsg {
			return true
		}
	}
	return false
}

// genOpencodeID mirrors 9router's genId(prefix): (ms*0x1000+counter) rendered
// as 12 hex chars (low 6 bytes, big-endian) + 14 alphanumerics.
// Produces ses_/msg_ IDs matching /^(ses|msg)_[0-9a-f]{12}[0-9A-Za-z]{14}$/.
// Note: at current epoch ms*0x1000 needs ~53 bits, so the low-48-bit slice
// truncates the high bits — IDs are unique per process (counter + crypto
// rand suffix) but not timestamp-decodable/monotonic long-term. Kept at 12
// hex chars for 9router parity.
var (
	opencodeIDMu      sync.Mutex
	opencodeIDLast    int64
	opencodeIDCounter int64
)

const opencodeIDChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func genOpencodeID(prefix string) string {
	now := time.Now().UnixMilli()
	opencodeIDMu.Lock()
	if now != opencodeIDLast {
		opencodeIDLast = now
		opencodeIDCounter = 0
	}
	opencodeIDCounter++
	counter := opencodeIDCounter
	opencodeIDMu.Unlock()

	val := uint64(now)*0x1000 + uint64(counter)
	var timeBytes [8]byte
	binary.BigEndian.PutUint64(timeBytes[:], val)
	// Take the low 6 bytes, rendered as 12 hex chars.
	timeHex := hex.EncodeToString(timeBytes[2:])

	var rb [14]byte
	if _, err := rand.Read(rb[:]); err != nil {
		// Fallback: time-based padding (still matches charset).
		for i := range rb {
			rb[i] = byte((now + int64(i)) % 62)
		}
		var sb strings.Builder
		sb.WriteString(prefix)
		sb.WriteByte('_')
		sb.WriteString(timeHex)
		for _, b := range rb {
			sb.WriteByte(opencodeIDChars[int(b)%62])
		}
		return sb.String()
	}
	var sb strings.Builder
	sb.WriteString(prefix)
	sb.WriteByte('_')
	sb.WriteString(timeHex)
	for _, b := range rb {
		sb.WriteByte(opencodeIDChars[int(b)%62])
	}
	return sb.String()
}

// genProjectID returns 40 lowercase hex chars (20 random bytes).
// Mirrors 9router's generateProjectId (crypto.randomBytes(20).toString("hex")).
func genProjectID() string {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strings.Repeat("0", 40)
	}
	return hex.EncodeToString(b[:])
}

// genUUID returns a random RFC 4122 v4 UUID without pulling in a dependency.
// Kept for backward compat (tests / external callers).
func genUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-0000-0000-000000000000"
	}
	b[6] = (b[6] & 3) | 8<<4 // version 4
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
