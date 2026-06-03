# Use any-llm-go for request/response translation

**Date:** 2026-06-04
**Status:** Approved
**Scope:** Replace the hand-rolled OpenAI-compatible request/response byte pipeline in `internal/proxy` and `internal/upstream` with `github.com/mozilla-ai/any-llm-go` typed types and providers.

## Problem

freegate currently passes the raw OpenAI-format request body to each upstream and post-processes the response bytes (`internal/proxy/normalize.go`, 201 lines of `bufio`/`map[string]any`/`syncReasoning` to drop the `reasoning` field alias). This:

- Couples request/response shape to byte-level string manipulation.
- Doesn't expose typed support for `tools`, `response_format`, `reasoning_effort`, multimodal `content`, or `usage.reasoning_tokens` — fields that any-llm-go gives us for free.
- Forces retry logic to look at `resp.StatusCode == 429` instead of typed error sentinels.
- Means adding a new OpenAI-compatible upstream requires another hand-rolled `Upstream` impl.

`mozilla-ai/any-llm-go` v0.9.0 provides a unified `Provider` interface, native `Message` / `ToolCall` / `Reasoning` / `Usage` types, and a configurable `*http.Client` (which is how we keep Tor/SOCKS5 working).

## Goals

1. Parse the incoming OpenAI request into a typed `anyllm.CompletionParams` in the handler.
2. Replace `OpenCodeUpstream` and `KiloUpstream` with thin `anyllmProvider` adapters that wrap `openai.New(WithBaseURL, WithAPIKey, WithHTTPClient)`.
3. Replace the byte-level response pipeline in `internal/proxy/normalize.go` with a typed `serialize.go` that marshals `*ChatCompletion` and `ChatCompletionChunk` straight to the wire.
4. Use typed `errors.Is(err, anyllm.ErrRateLimit)` for the 429 retry trigger.
5. Keep Tor SOCKS5 routing (inject our dialer via `anyllm.WithHTTPClient`).
6. Bump Go from 1.23 to 1.26 (any-llm-go requires it).

## Non-goals

- No change to the model routing rules (`Match` / `Router.Select`) — Kilo prefixes / `:free` suffix still route to Kilo.
- No change to the dashboard, recorder, rate limiter, auth, CORS, or Tor controller.
- No new upstream providers (no direct OpenAI/Anthropic). The library is the only new direct dependency.
- No new persisted fields. The "free filter" per upstream moves into a `freePred` callback on the shared `anyllmProvider`, not into the library.
- No support for multimodal content beyond what `Message.Content any` (string or `[]ContentPart`) already round-trips through the OpenAI SDK — the library handles this.

## Design

### Architecture

```
POST /v1/chat/completions
  → handler.Chat: json.Unmarshal(body, &anyllm.CompletionParams)
  → proxy.ProxyChat(w, r, params)
      → router.Select(params.Model) → anyllmProvider
      → for attempt in 0..maxRetry:
          if attempt>0: ipRotator.ForceNewIP(), sleep RetryDelay
          if stream:
              provider.CompletionStream(ctx, params) → chan ChatCompletionChunk, chan error
              writeStreaming(...)
          else:
              provider.Completion(ctx, params) → *ChatCompletion, error
              writeNonStreaming(...)
          on errors.Is(err, anyllm.ErrRateLimit): retry
      → metrics: usage.Total/Prompt/Completion → c.metrics.TotalTokens/...
  → tor  →  opencode.ai / api.kilo.ai
```

### Components

**`internal/upstream/anyllm.go` (new)**

```go
type anyllmProvider struct {
    name     string
    prefixes []string
    provider providers.Provider
    cache    *ModelCache
    freePred func(providers.Model) bool
}

func newAnyLLMProvider(name, baseURL, apiKey, socksAddr string,
    headers map[string]string, prefixes []string,
    freePred func(providers.Model) bool) *anyllmProvider
```

- Builds a Tor-routed `*http.Client` via a `newTorClient(socksAddr, headers)` helper extracted from `client.go:NewHTTPClient`.
- Creates the provider via `openai.New(WithAPIKey, WithBaseURL, WithHTTPClient)` with `CompatibleConfig.Name = name`.
- `Name() string` → `name`.
- `Match(modelID) bool`:
  - OpenCode (`prefixes == nil`): always true (default upstream).
  - Kilo: true if `modelID` ends in `":free"` or starts with any prefix in `prefixes`.
- `Models() []model.Model` → `cache.Get()`.
- `Start(ctx, refreshInterval)` → keep using `Refresher`, calling `ListModels`.
- `ListModels(ctx) ([]model.Model, error)` → `provider.ListModels(ctx)`, apply `freePred` to each item, return `[]model.Model`.
- `ChatCompletion` and `CompletionStream` are not implemented — the proxy talks to `provider` directly through the new `proxy.ProxyChat(params)` signature, not through the `Upstream` interface.

> Note: the existing `Upstream` interface (`Name/Match/ListModels/ChatCompletion/Models/Start`) shrinks. The new interface used by the proxy is:
> ```go
> type chatUpstream interface {
>     Name() string
>     Match(modelID string) bool
>     Models() []model.Model
>     Start(ctx context.Context, refreshInterval time.Duration)
>     provider() anyllm.Provider  // unexported, used by proxy
> }
> ```
> The `Router` and `Refresher` still consume this; only the proxy's call to `u.ChatCompletion(ctx, body)` is replaced by direct use of `u.provider()`.

**`internal/proxy/serialize.go` (new, ~80 lines)**

```go
type TokenUsage struct { Prompt, Completion, Total int }

func writeNonStreaming(w http.ResponseWriter, resp *anyllm.ChatCompletion, usage *TokenUsage)
func writeStreaming(w http.ResponseWriter, chunks <-chan anyllm.ChatCompletionChunk, errs <-chan error, usage *TokenUsage) error
```

- `writeNonStreaming`: `w.Header().Set("Content-Type", "application/json")`, `w.WriteHeader(200)`, `json.Marshal(resp)` → `w.Write`. Also `w.Header().Set("Access-Control-Allow-Origin", "*")`.
- `writeStreaming`: set `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`, `Access-Control-Allow-Origin: *`, `WriteHeader(200)`, `fl.Flush()`. Loop: for each chunk, `json.Marshal` → `fmt.Fprintf(w, "data: %s\n\n", b)` → `fl.Flush()`. Update `usage` from `chunk.Usage`. On channel close, write `data: [DONE]\n\n` and flush. On `errs` receive, return the error.
- If `writeStreaming` returns an error AND the writer hasn't sent any data yet, the caller can fall through to `respond.JSONError` with the corresponding status code.

**`internal/proxy/normalize.go`** — deleted. `TokenUsage` moves to `serialize.go`.

**`internal/handler/handler.go`** — `Chat` decodes the full `anyllm.CompletionParams`:
```go
var params anyllm.CompletionParams
if err := json.Unmarshal(body, &params); err != nil {
    respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error()); return
}
if params.Model == "" {
    respond.JSONError(w, http.StatusBadRequest, "bad_request", "missing required field: model"); return
}
if len(params.Messages) == 0 {
    respond.JSONError(w, http.StatusBadRequest, "bad_request", "messages is required and must be non-empty"); return
}
h.upstream.ProxyChat(w, r, params)
```
The `Upstream` interface used by `Handler` widens to include the new `ProxyChat(w, r, params)` signature.

**`internal/proxy/proxy.go`** — `ProxyChat(w http.ResponseWriter, r *http.Request, params anyllm.CompletionParams)`:
- Drop the `modelID string, body []byte` parameters; `params.Model` is now the source of truth.
- Drop the `copyHeaders` hop-by-hop logic; we set our own headers in the serializer.
- Retry loop:
  ```go
  for attempt := 0; attempt <= c.maxRetry; attempt++ {
      if attempt > 0 {
          if c.ipRotator != nil { c.ipRotator.ForceNewIP() }
          c.metrics.RetryCount.Add(1)
          select {
          case <-r.Context().Done():
              respond.JSONError(w, http.StatusGatewayTimeout, "client_closed", "client disconnected during retry"); return
          case <-time.After(RetryDelay):
          }
      }
      if params.Stream {
          err := c.runStream(w, r, u, params, requestID, start, &usage, &finalStatus, &finalErr, &finalTotal, &finalPrompt, &finalCompletion)
          if errors.Is(err, anyllm.ErrRateLimit) { continue }
          // success or non-retryable: return
      } else {
          resp, err := u.provider().Completion(r.Context(), params)
          if errors.Is(err, anyllm.ErrRateLimit) { continue }
          if err != nil { ... 502 ...; return }
          finalStatus = 200
          writeNonStreaming(w, resp, &usage)
          break
      }
  }
  ```
- Metrics + request-log defer stays the same.

**`internal/upstream/client.go`** — extract the SOCKS5 dialer setup into `newTorClient(socksAddr string, headers map[string]string) *http.Client`. The existing `NewHTTPClient` and `Post`/`Get`/`ReadAll` helpers are removed (any-llm-go's openai-go SDK owns the HTTP path now). One helper, used by `anyllmProvider` constructor, returns the `*http.Client` to pass to `anyllm.WithHTTPClient`.

**`cmd/server/main.go`** — replace the `NewOpenCodeUpstream` / `NewKiloUpstream` constructors with:
```go
opencode := upstream.NewAnyLLMProvider("opencode", cfg.UpstreamURLOpenCode, cfg.UpstreamKeyOpenCode, cfg.SOCKSAddr,
    map[string]string{"x-opencode-client": "desktop"}, nil, openCodeIsFree)
kilo := upstream.NewAnyLLMProvider("kilo", cfg.UpstreamURLKilo, cfg.UpstreamKeyKilo, cfg.SOCKSAddr,
    nil, cfg.UpstreamKiloPrefixes, kiloIsFree)
```

**`go.mod` / `Dockerfile`**:
- `go 1.26.0`
- `require github.com/mozilla-ai/any-llm-go v0.9.0` (transitive deps via `go mod tidy`).
- `FROM golang:1.26-alpine` in Dockerfile.

**`README.md`** — remove the "Reasoning Normalization" section, update the tech stack to list `any-llm-go`.

### Data flow

**Non-streaming request:**

1. Client → `POST /v1/chat/completions` (OpenAI JSON; may include `tools`, `response_format`, `reasoning_effort`, multimodal `content`).
2. `handler.Chat`: `json.Unmarshal` → `anyllm.CompletionParams`. Validation: `Model != ""`, `len(Messages) > 0`. 400 with `bad_request` on failure.
3. `proxy.ProxyChat`: `router.Select(params.Model)` → `anyllmProvider`.
4. Loop (max `DefaultMaxRetry + 1` = 3 attempts):
   - if `attempt > 0`: `ipRotator.ForceNewIP()` (if available), `metrics.RetryCount.Add(1)`, sleep `RetryDelay` (or return 504 on client disconnect).
   - `resp, err := u.provider().Completion(r.Context(), params)`.
   - if `errors.Is(err, anyllm.ErrRateLimit)`: continue.
   - if other err: `metrics.UpstreamErrors.Add(1)`, `respond.JSONError(502, "upstream_error", ...)`, return.
   - on success: break.
5. `writeNonStreaming(w, resp, &usage)`: set headers, `WriteHeader(200)`, `json.Marshal(resp)` → `w.Write`.
6. Metrics: `usage.Total/Prompt/Completion` → `c.metrics.TotalTokens/...`.

**Streaming request:**

1. Same as above through step 3.
2. `chunks, errs := u.provider().CompletionStream(r.Context(), params)`.
3. `writeStreaming(w, chunks, errs, &usage)`:
   - Set SSE headers + `Access-Control-Allow-Origin: *`, `WriteHeader(200)`, flush.
   - Loop on `chunks`: marshal, write `data: ...\n\n`, flush. Update `usage` from `chunk.Usage`.
   - On `errs` receive: if no data has been written yet, return error to caller (caller will fall through to `respond.JSONError`). Otherwise log and close.
   - On `chunks` channel close: write `data: [DONE]\n\n`, flush.
4. Final status is 200 if the first chunk was sent successfully, or the error from `errs` otherwise.

**Model listing (`GET /v1/models`):** unchanged externally. `anyllmProvider.Models()` returns the cache. The cache is populated by `Refresher` → `anyllmProvider.ListModels` → `provider.ListModels` → `freePred` filter → `cache.Set`.

**Retry on 429:** same `3s` delay, same `2` retries, same Tor IP rotation. Only the trigger changes from `resp.StatusCode == 429` to `errors.Is(err, anyllm.ErrRateLimit)`.

### Error handling

| Condition | Non-streaming | Streaming (pre-first-chunk) | Streaming (post-first-chunk) |
|---|---|---|---|
| `anyllm.ErrRateLimit` | retry with IP rotation | retry with IP rotation | log + close (can't recover) |
| `anyllm.ErrAuthentication` | 502 `upstream_error` | 502 `upstream_error` | log + close |
| `anyllm.ErrContextLength` | 502 with message | 502 with message | log + close |
| `anyllm.ErrModelNotFound` | 502 with message | 502 with message | log + close |
| `anyllm.ErrContentFilter` | 502 with message | 502 with message | log + close |
| Other error | 502 `upstream_error` | 502 `upstream_error` | log + close |
| Client disconnect mid-retry | 504 `client_closed` | 504 `client_closed` | close (already mid-stream) |
| Client disconnect mid-stream | n/a | n/a | `r.Context().Done()` propagates; goroutine in any-llm-go surfaces `ctx.Err()`; loop breaks |

The "can't recover after first chunk" rule is a property of HTTP/SSE, not a regression. The current code does the same — once headers are flushed, only connection close is possible.

## Testing

TDD throughout. Tests live next to the code they exercise.

### `internal/upstream/anyllm_test.go`

- `TestAnyLLMProvider_Match`: opencode always true; kilo matches `kilo/...`, `kilo-...`, `openrouter/...`, and any model whose ID ends in `:free`; kilo does not match `deepseek-v4-flash-free` (no kilo prefix, no `:free` suffix) — the opencode default upstream handles it. The test pins the existing routing contract.
- `TestAnyLLMProvider_Name`: returns `"opencode"` and `"kilo"`.
- `TestAnyLLMProvider_ListModels_FreeFilter`: use `httptest.NewServer` returning `{"object":"list","data":[{...},...]}`. Two sub-tests:
  - `opencode`: `freePred` returns true only for `Cost == "0"` or `-free` suffix.
  - `kilo`: `freePred` returns true only when `isFree == true`.
- `TestAnyLLMProvider_Completion_ForwardsParams`: `httptest.NewServer` records the body and asserts the JSON contains the model, messages, temperature, max_tokens, and tools exactly as set in the `CompletionParams`.
- `TestAnyLLMProvider_CompletionStream_ForwardsChunks`: server emits three SSE events then `[DONE]`; assert each non-DONE event reaches the client correctly (we test this at the `serialize` layer instead, see below).

### `internal/proxy/serialize_test.go`

- `TestWriteNonStreaming_MarshalAndUsage`: feed a `*ChatCompletion` with `Usage`; assert `Content-Type: application/json`, `Access-Control-Allow-Origin: *`, body is the exact JSON of the input, `usage` struct populated.
- `TestWriteStreaming_ChunksAndDone`: feed a 3-chunk channel + closed `errs`; assert `Content-Type: text/event-stream`, each chunk framed as `data: {...}\n\n`, final `data: [DONE]\n\n`.
- `TestWriteStreaming_ErrorBeforeFirstChunk`: feed an empty `chunks` channel and a buffered `errs` with `ErrRateLimit`; assert the function returns the error and writes nothing.
- `TestWriteStreaming_FlushesPerChunk`: use a `mockResponseWriter` with a `Flush()` counter to confirm each chunk triggers a flush.

### `internal/proxy/proxy_test.go` (new)

Table-driven retry tests, using a fake `anyllmProvider` that returns scripted responses/errors:

- `RateLimitThenSuccess`: first call returns `RateLimitError`, second returns OK. Assert `RetryCount == 1`, response is the success body, `ipRotator.ForceNewIP` called once.
- `AuthError`: returns `AuthenticationError`. Assert 502 returned, `UpstreamErrors == 1`, no retry, no IP rotation.
- `AllRateLimited`: three `RateLimitError` in a row. Assert 502 returned, `RetryCount == 2`, `ipRotator.ForceNewIP` called twice.
- `ContextLengthError`: returns `ContextLengthError`. Assert 502, no retry.
- `StreamRateLimitRetry`: scripted `CompletionStream` returns `ErrRateLimit` on the first attempt, then chunks. Assert 200, `data: ...` lines present.

### `internal/handler/handler_test.go` (extend)

- `TestChat_DecodesCompletionParams`: request with `tools`, `temperature`, `max_tokens`; assert `ProxyChat` is called with a `CompletionParams` whose `Tools`/`Temperature`/`MaxTokens` are populated.
- `TestChat_RejectsEmptyMessages`: empty `messages` → 400.
- `TestChat_RejectsMissingModel`: empty `model` → 400.
- `TestChat_RejectsInvalidJSON`: malformed body → 400.

### Existing tests kept

- `internal/proxy/normalize_test.go` is deleted along with the file.
- `internal/upstream/{kilocode,opencode}_test.go` are deleted; their coverage moves to `anyllm_test.go` and the `freePred` callback's own unit tests.
- All other test files (`middleware_test`, `metrics_test`, `model_test`, `respond_test`, `tor_test`, `ringbuffer_test`, `ui_test`, `config_test`, `collector_test`, `client_test`) remain. `client_test.go` will lose tests of the removed `Post`/`Get`/`ReadAll` helpers; the dialer test stays.

### Manual smoke (after `go test ./...` passes)

```bash
docker compose build
docker compose up -d
curl -N -X POST http://localhost:1234/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":20}'
curl -N -X POST http://localhost:1234/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"kilo/llama-3.3-70b-versatile:free","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":20}'
curl http://localhost:1234/v1/models
```

## Risks and mitigations

- **Go 1.26 toolchain availability.** CI/Docker must use 1.26+. `go mod tidy` after the bump will pull a `toolchain` directive; Dockerfile pins `golang:1.26-alpine`. README updated.
- **Hidden behavior in openai-go SDK.** The openai-go SDK does not parse the `reasoning` / `reasoning_content` fields on `ChatCompletion.Message` (those are non-standard). With the field rename dropped per the design choice, neither field will appear in our output JSON. **This is a documented behavior change.** Models that previously emitted reasoning content will no longer have it visible to clients. The README's "Reasoning Normalization" section is removed. If we later need to surface reasoning, the path is a small post-processor that runs a `json.Unmarshal` of the upstream body and copies the field — but that's out of scope.
- **Library version churn.** any-llm-go is pre-1.0. Pin `v0.9.0` exactly via `require ... // indirect` won't be needed; `go get github.com/mozilla-ai/any-llm-go@v0.9.0` then `go mod tidy`.
- **SOCKS5 in tests.** `httptest.NewServer` listens on `127.0.0.1` over plain HTTP; tests pass `WithHTTPClient(http.DefaultClient)` and skip the SOCKS5 dialer. The production dialer path is exercised in the manual smoke.
- **Streaming `WriteHeader` race.** If we get an `errs` value before sending the first chunk, we must NOT have called `WriteHeader(200)`. The serializer must return an error in that case so the caller can fall through to `respond.JSONError`. The test `TestWriteStreaming_ErrorBeforeFirstChunk` pins this contract.
- **Channel closing order.** any-llm-go's `CompletionStream` closes `chunks` first, then `errs`. The serializer consumes `chunks` in a `for chunk := range chunks` loop and reads `errs` separately. If `errs` is non-nil AND the channel is closed, the caller's `finalErr` is set; the proxy logs it and returns 502 in the non-stream case. In the stream case, the response has already been sent and the error is logged only.

## Open questions

None. All design decisions resolved during brainstorming:

1. Go version → bump to 1.26.
2. Scope → replace both upstreams.
3. Streaming output → serialize `ChatCompletionChunk` → SSE on the fly.
4. Reasoning field handling → drop the rename; library emits whatever it captures.
