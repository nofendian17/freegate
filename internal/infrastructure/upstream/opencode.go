package upstream

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"freegate/internal/domain"
	"freegate/internal/infrastructure/upstream/types"
	"freegate/internal/translate"
)

// OpenCode Zen client identity (User-Agent version cache, canonical
// session IDs) lives in opencode_identity.go, mirroring 9router PR #10
// (cherry-pick of decolua#4105) for the gateway's free-tier validation.

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
	// IDs per call, desktop client tag, global project, and anthropic-version
	// for /messages models.
	headers := map[string]string{
		"x-opencode-client": "desktop",
		"User-Agent":        openCodeUserAgent(),
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
	// Client-version probe runs alongside the model catalog refresher so
	// the advertised User-Agent tracks OpenCode releases. Fail-open: a
	// failed probe keeps the fallback version.
	go NewRefresher("opencode-client", refreshOpenCodeClientVersion, openCodeClientVersionTTL).Run(ctx)
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
	// Anonymous free-tier requests are only served as streams (verified
	// live: non-streaming bodies get 403 FreeTierError on every endpoint).
	// Upgrade non-streaming anonymous requests to stream:true and fold the
	// SSE back into one native JSON object so callers see no difference.
	// Keyed requests keep native behavior.
	if !isStreamBody(out) && o.anonymousOnly() {
		out = ensureStreamRequest(out, endpoint)
		return o.chatCompletionStreamAssembled(ctx, endpoint, out)
	}
	headers := buildOpencodeHeaders(ctx, endpoint, out)
	logZenRequest(endpoint, headers, out)
	// x-api-key/Authorization sync is handled per attempt inside
	// HTTPClient.doWithHeaders: when Authorization carries a non-public key
	// but x-api-key is still the default "public", the client mirrors the
	// bearer key into x-api-key so 429 failover never sends a mismatched pair.
	resp, err := o.client.PostWithHeaders(ctx, endpoint, out, headers)
	if err != nil {
		return nil, err
	}
	result := domain.NewUpstreamResponse(resp)
	result.Format = string(o.RequestFormat(out))
	return result, nil
}

// anonymousOnly reports whether every configured key is the anonymous
// public marker (or unset). Deliberately configuration-based, not
// selection-based: peeking at key rotation would advance it (currentKey
// increments the round-robin counter), so the upgrade decision could end
// up using a different key than the request.
func (o *OpenCodeUpstream) anonymousOnly() bool {
	if o == nil || o.client == nil {
		return true
	}
	keys := o.client.apiKeys
	if len(keys) == 0 {
		return true
	}
	for _, k := range keys {
		if k != "" && k != "public" {
			return false
		}
	}
	return true
}

// ensureStreamRequest enables streaming on a non-streaming body.
// stream_options.include_usage is OpenAI-chat-only: Messages and Responses
// bodies just get stream:true, which both natively support.
func ensureStreamRequest(body []byte, endpoint string) []byte {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	raw["stream"] = true
	if strings.HasSuffix(endpoint, "/chat/completions") {
		if _, ok := raw["stream_options"]; !ok {
			raw["stream_options"] = map[string]any{"include_usage": true}
		}
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}

// chatCompletionStreamAssembled posts a streaming request and folds the SSE
// back into one native JSON response. Non-SSE replies (e.g. JSON errors)
// pass through untouched for the usual failover handling.
func (o *OpenCodeUpstream) chatCompletionStreamAssembled(ctx context.Context, endpoint string, out []byte) (*domain.UpstreamResponse, error) {
	headers := buildOpencodeHeaders(ctx, endpoint, out)
	logZenRequest(endpoint, headers, out)
	resp, err := o.client.PostWithHeaders(ctx, endpoint, out, headers)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("opencode: nil upstream response")
	}
	// Only assemble 200 SSE streams. Anything else (e.g. a JSON error with
	// an unusual content type) passes through untouched so failover sees
	// the real status and body.
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		result := domain.NewUpstreamResponse(resp)
		result.Format = string(o.RequestFormat(out))
		return result, nil
	}
	defer resp.Body.Close()
	model := extractOpencodeModel(out)
	assembled, err := assembleUpstreamStream(endpoint, resp.Body, model, MaxResponseBodySize)
	if err != nil {
		return nil, fmt.Errorf("opencode: assemble stream: %w", err)
	}
	header := resp.Header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	header.Set("Content-Type", "application/json")
	header.Del("Content-Length")
	return &domain.UpstreamResponse{
		Format:     string(o.RequestFormat(out)),
		StatusCode: resp.StatusCode,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(assembled)),
	}, nil
}

func (o *OpenCodeUpstream) RequestFormat(body []byte) translate.Format {
	switch o.buildURL(extractOpencodeModel(body), body) {
	case "/messages":
		return translate.FormatClaude
	case "/responses":
		return translate.FormatOpenAIResponses
	default:
		return translate.FormatOpenAI
	}
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

// genSessionID returns an x-opencode-session in canonical descending form:
// ses_ + 12 lowercase hex chars (low 6 bytes of ~(ms*0x1000 + counter)) +
// 14 Base62 chars. The bitwise complement makes lexicographic order
// reverse-chronological; the gateway validates this shape for free-tier
// requests (9router PR #10). Uses the shared module counter.
// genRequestID returns an x-opencode-request in canonical form:
// msg_ + 12 lowercase hex chars (low 6 bytes of ms*0x1000 + 1, no
// complement) + 14 Base62 chars. The counter is fixed at 1 and the shared
// module counter is left untouched, mirroring 9router exactly.
func genSessionID() string {
	return genOpencodeIDWithClock("ses", true)
}

func genRequestID() string {
	val := uint64(time.Now().UnixMilli())*0x1000 + 1
	var timeBytes [8]byte
	binary.BigEndian.PutUint64(timeBytes[:], val)
	timeHex := hex.EncodeToString(timeBytes[2:])
	var sb strings.Builder
	sb.WriteString("msg_")
	sb.WriteString(timeHex)
	var rb [14]byte
	if _, err := rand.Read(rb[:]); err != nil {
		for i := range rb {
			rb[i] = byte(i % 62)
		}
	}
	for _, b := range rb {
		sb.WriteByte(opencodeIDChars[int(b)%62])
	}
	return sb.String()
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
// PR #4111 and PR #10: Bearer public + x-api-key public, versioned
// first-party UA, desktop client tag, canonical session/request IDs,
// global project, streaming Accept, and anthropic-version for /messages.
func buildOpencodeHeaders(ctx context.Context, endpoint string, body []byte) map[string]string {
	stream := isStreamBody(body)
	isMessages := strings.HasSuffix(endpoint, "/messages")
	accept := "*/*"
	if stream {
		accept = "text/event-stream"
	}
	id := translate.DownstreamIdentityFrom(ctx)
	ua := openCodeUserAgent()
	if translate.ValidOpencodeVersion(id.UserAgent) {
		ua = id.UserAgent
	}
	session := genSessionID()
	if id.Session != "" {
		tool := id.Client
		if tool == "" {
			tool = "generic"
		}
		session = translate.TranslateSessionID(id.Session, tool)
	}
	// Downstream request/client/project values pass through verbatim when
	// present. Unlike sessions they are NOT canonicalized: genuine clients
	// send stable non-canonical values here (account-bound user IDs, real
	// project hashes), and forcing them into msg_/canonical shape would
	// destroy exactly the identity a chained genuine client provides.
	// Lengths are already capped at extraction (ClipIdentity).
	request := genRequestID()
	if id.RequestID != "" {
		request = id.RequestID
	}
	client := "desktop"
	if id.Client != "" {
		client = id.Client
	}
	project := "global"
	if id.Project != "" {
		project = id.Project
	}
	headers := map[string]string{
		"Content-Type":       "application/json",
		"x-api-key":          "public",
		"User-Agent":         ua,
		"x-opencode-client":  client,
		"x-opencode-session": session,
		"x-opencode-request": request,
		"x-opencode-project": project,
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

// genOpencodeID mirrors 9router's request ID generation:
// (ms*0x1000+counter) rendered as 12 hex chars (low 6 bytes, big-endian)
// + 14 alphanumerics. Produces msg_ IDs matching
// /^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$/. Session IDs (ses_) additionally
// apply the canonical bitwise complement — see genSessionID.
var (
	opencodeIDMu      sync.Mutex
	opencodeIDLast    int64
	opencodeIDCounter int64
)

const opencodeIDChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func genOpencodeID(prefix string) string {
	return genOpencodeIDWithClock(prefix, false)
}

func genOpencodeIDWithClock(prefix string, complement bool) string {
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
	if complement {
		val = ^val
	}
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
