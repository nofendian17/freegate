# Clean Architecture Refactor — Freegate

**Date:** 2026-06-04
**Status:** Approved design, pending implementation
**Goal:** Restructure freegate into ports-and-adapters (DDD) layering while fixing bugs, eliminating duplication, and improving test coverage.

---

## 1. Principles

1. **Dependencies point inward.** The `domain` package has zero imports from other internal packages. Infrastructure depends on domain. Application orchestrates domain.
2. **No circular dependencies.** Enforced by layer rules.
3. **No duplication.** Shared HTTP utilities live in `httputil`.
4. **No god objects.** `main.go` → `server.go`. `to_claude.go` → 3 focused files.
5. **Tests follow code.** Every moved/refactored file gets its tests moved alongside it.
6. **YAGNI.** No abstract factory, no DI container, no event bus. Go interfaces + constructor injection.

---

## 2. Layer Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    delivery/                                 │
│  handler/  middleware/  respond/  ui/                       │
│  (HTTP layer — depends on application/)                     │
├─────────────────────────────────────────────────────────────┤
│                    application/                              │
│  ChatService / ModelService / MetricsService                │
│  (use-case orchestration — depends on domain/)              │
├─────────────────────────────────────────────────────────────┤
│                    domain/                                   │
│  Upstream / IPRotator / RequestLogger interfaces            │
│  Model / ModelList / ErrorResp types                        │
│  (innermost — zero internal imports)                        │
├─────────────────────────────────────────────────────────────┤
│                    infrastructure/                           │
│  upstream/  tor/  proxy/  metrics/  recorder/  ringbuffer/  │
│  (adapters — implement domain interfaces)                   │
└─────────────────────────────────────────────────────────────┘

Cross-cutting: config/  translate/  server/  httputil/  web/
```

---

## 3. Package Map

### 3.1 `domain/` — Ports and Core Types

**Purpose:** Innermost layer. Zero imports from other freegate packages.

**Files:**

| File | Contents | Origin |
|------|----------|--------|
| `model.go` | `Model`, `ModelList`, `ErrorResp`, `NewError()` | `internal/model/model.go` |
| `upstream.go` | `Upstream` interface (`Name`, `Match`, `ListModels`, `ChatCompletion`, `Models`, `Start`) | `internal/upstream/upstream.go` (interface only) |
| `ip_rotator.go` | `IPRotator` interface (`NewIP`, `ForceNewIP`, `CurrentIP`) | `internal/tor/tor.go` (interface extracted) |
| `request_log.go` | `RequestLogEntry`, `RequestLogger` type | `internal/model/request_log.go` |
| `timeseries.go` | `TimeseriesEntry` type | `internal/model/timeseries.go` |
| `errors.go` | Domain errors: `ErrModelNotFound`, `ErrEmptyRequestBody`, `ErrBodyTooLarge` | `internal/model/model.go` |

**What is NOT here:**
- `KiloModel`, `KiloModelList`, `OpenCodeModel`, `OpenCodeModelList` — moved to `infrastructure/upstream/*_types.go`
- HTTP types, config types, middleware types

### 3.2 `application/` — Use-Case Services

**Purpose:** Orchestrate domain interfaces. No HTTP or infrastructure awareness.

**Files:**

| File | Contents | Origin |
|------|----------|--------|
| `chat.go` | `ChatService` — select upstream, retry loop with IP rotation, call proxy normalize, log | Extracted from `internal/proxy/proxy.go` `ProxyChat()` |
| `models.go` | `ModelService` — `AllModels()` (merge/dedup from all routers), `IsReady()` | Extracted from `internal/upstream/router.go` + `internal/handler/handler.go` |
| `metrics.go` | `MetricsService` — wrapper around atomic counters with typed snapshot | Wraps `internal/metrics/metrics.go` |

**`ChatService.ProxyChat` flow:**
```
1. Incr TotalRequests counter
2. router.Select(modelID) → upstream
3. Incr upstream counter
4. Retry loop (maxRetries + 1):
   a. upstream.ChatCompletion(ctx, req) → *http.Response
   b. On 429: ipRotator.ForceNewIP(); continue
   c. On connection error: return err
   d. On success: break
5. Copy response headers to http.ResponseWriter
6. proxy.NormalizeResponse(w, resp) — handles SSE/JSON + reasoning sync
7. Log request with status/duration/tokens
```

### 3.3 `infrastructure/` — Adapters

**Purpose:** Implement domain interfaces. No imports from `application/` or `delivery/`.

#### 3.3.1 `infrastructure/upstream/`

| File | Contents | Origin |
|------|----------|--------|
| `router.go` | `Router` struct — `Select()`, `AllModels()`, `IsReady()` | `internal/upstream/router.go` |
| `client.go` | `HTTPClient` — SOCKS5 HTTP client with `Post()` | `internal/upstream/client.go` |
| `cache.go` | `ModelCache` — thread-safe model cache | `internal/upstream/cache.go` |
| `opencode.go` | `OpenCodeUpstream` — implements `domain.Upstream` | `internal/upstream/opencode.go` |
| `opencode_types.go` | `OpenCodeModel`, `OpenCodeModelList` — upstream-specific parse types | Extracted from `internal/model/model.go` |
| `kilocode.go` | `KiloUpstream` — implements `domain.Upstream` | `internal/upstream/kilocode.go` |
| `kilocode_types.go` | `KiloModel`, `KiloModelList` — upstream-specific parse types | Extracted from `internal/model/model.go` |
| `refresher.go` | `Refresher` — periodic model fetch with backoff | `internal/upstream/refresher.go` |

#### 3.3.2 `infrastructure/tor/`

| File | Contents | Origin |
|------|----------|--------|
| `controller.go` | `Controller` — implements `domain.IPRotator` | `internal/tor/tor.go` |

#### 3.3.3 `infrastructure/proxy/`

| File | Contents | Origin |
|------|----------|--------|
| `client.go` | HTTP transport helpers. Lower-level HTTP call + header management. | Extracted from `internal/proxy/proxy.go` |
| `normalize.go` | `copyNormalized`, `normalizeStream`, `normalizeJSON`, `syncReasoning` — SSE/JSON normalization | `internal/proxy/normalize.go` |

#### 3.3.4 `infrastructure/metrics/`

| File | Contents | Origin |
|------|----------|--------|
| `counter.go` | `Metrics` — atomic counters, `Snapshot()`, `IncrUpstream()`, `LogStats()` | `internal/metrics/metrics.go` |

#### 3.3.5 `infrastructure/recorder/`

| File | Contents | Origin |
|------|----------|--------|
| `recorder.go` | `Recorder` — request log ring buffer, timeseries sampling, models/TorIP callbacks | `internal/collector/recorder.go` |

#### 3.3.6 `infrastructure/ringbuffer/`

| File | Contents | Origin |
|------|----------|--------|
| `ringbuffer.go` | `RingBuffer[T]` — generic thread-safe ring buffer | `internal/ringbuffer/ringbuffer.go` |

### 3.4 `delivery/` — HTTP Layer

#### 3.4.1 `delivery/handler/`

| File | Contents | Origin |
|------|----------|--------|
| `handler.go` | `Handler` struct + `Routes()` chi setup + shared helpers | `internal/handler/handler.go` (reduced) |
| `chat.go` | `Handler.Chat()` — parse body, detect format, translate, delegate to ChatService | Extracted |
| `models.go` | `Handler.ListModels()` — returns merged model list | Extracted |
| `ready.go` | `Handler.Ready()` — health check | Extracted |
| `metrics.go` | `Handler.Metrics()` — JSON metrics snapshot | Extracted |
| `root.go` | `Handler.Root()` — endpoint listing | Extracted |

#### 3.4.2 `delivery/middleware/`

| File | Contents | Origin |
|------|----------|--------|
| `middleware.go` | `Logger`, `CORS`, `Recoverer`, `RequestID`, `Auth`, `RateLimiter` | `internal/middleware/middleware.go` |

**Fix:** `Recoverer()` and `Auth()` now use `delivery/respond` for JSON responses instead of raw `w.Write()`.

#### 3.4.3 `delivery/respond/`

| File | Contents | Origin |
|------|----------|--------|
| `respond.go` | `JSONError()`, `JSON()`, `Ready()` | `internal/respond/respond.go` |

#### 3.4.4 `delivery/ui/`

| File | Contents | Origin |
|------|----------|--------|
| `handler.go` | `Handler` — UI routes | `internal/ui/handler.go` |
| `dashboard.go` | Full dashboard render + page data | `internal/ui/dashboard.go` |
| `partials.go` | HTMX partial renderers | `internal/ui/partials.go` |
| `loader.go` | `LoadTemplates()` | `internal/ui/loader.go` |

### 3.5 `translate/` — Format Translation

| File | Contents | Origin |
|------|----------|--------|
| `translate.go` | `Format` type, constants | `internal/translate/translate.go` |
| `detect.go` | `Detect()`, `ExtractModelID()`, `IsStreaming()` | `internal/translate/detect.go` |
| `request.go` | `Request()` — format-aware request translation | `internal/translate/request.go` |
| `response.go` | `ResponseJSON()` — format-aware JSON response translation | `internal/translate/response.go` |
| `writer.go` | `ResponseWriter` — wrapping HTTP writer for streaming translation | Extracted |
| `claude/request.go` | `claudeToOpenAI()` | `internal/translate/claude_request.go` |
| `claude/stream.go` | SSE state machine + `processOpenAIChunk()` | Extracted from `internal/translate/to_claude.go` |
| `claude/json.go` | `openaiJSONToClaude()` + usage extraction | Extracted from `internal/translate/to_claude.go` |
| `gemini/request.go` | `geminiToOpenAI()` | `internal/translate/gemini_request.go` |
| `gemini/stream.go` | `processGeminiChunk()` | `internal/translate/to_gemini.go` |

### 3.6 `server/` — Application Wiring

| File | Contents |
|------|----------|
| `server.go` | `Server` struct with `New(cfg)`, `Run(ctx)`, `Shutdown(ctx)` |

**`New()` wires:**
1. Config (already loaded)
2. Tor Controller
3. Upstreams (OpenCode + Kilo) + ModelCaches + Refreshers
4. Router
5. Proxy normalize helpers
6. ChatService + ModelService + MetricsService
7. Recorder
8. UI templates + UI Handler
9. API Handler (depends on ChatService, ModelService, MetricsService)
10. RateLimiter
11. Chi router with middleware + routes
12. Tor IP monitor goroutine

`cmd/server/main.go` becomes:
```go
func main() {
    cfg := config.Load()
    srv, err := server.New(cfg)
    if err != nil { log.Fatal(err) }
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()
    if err := srv.Run(ctx); err != nil { log.Fatal(err) }
}
```

### 3.7 `httputil/` — Shared Utilities

| File | Contents | Origin |
|------|----------|--------|
| `ip.go` | `ClientIP(r *http.Request) string` — single source of truth | Deduplicated from `middleware.go` + `proxy.go` |
| `header.go` | `CopyHeaders(dst, src http.Header)` | Deduplicated from `proxy.go` + `to_claude.go` |
| `convert.go` | `Int64(val any) int64` | Deduplicated from `recorder.go` + `partials.go` |

### 3.8 `config/` — Configuration

| File | Contents | Origin |
|------|----------|--------|
| `config.go` | `Config` struct, `Load()`, `Validate()` | `internal/config/config.go` |

Unchanged. Cross-cutting concern with no freegate imports.

### 3.9 `web/` — Embedded Assets

Unchanged. `embed.go`, `templates/`, `static/`. Used by `delivery/ui/`.

---

## 4. Bugs Fixed

### B1: Retry loop leaks HTTP response bodies

**Location:** `application/chat.go`

**Problem:** Proxy retry loop overwrites `resp` without closing previous body. `defer resp.Body.Close()` in the outer scope only closes the last response.

**Fix:** Ensure each failed response body is closed before the next retry attempt:
```go
for attempt := 0; attempt <= maxRetries; attempt++ {
    resp, err := u.ChatCompletion(ctx, req)
    if err != nil { return err }
    if resp.StatusCode != http.StatusTooManyRequests {
        return resp, nil  // caller owns close
    }
    resp.Body.Close()  // close before retry
    ipRotator.ForceNewIP()
}
```

### B2: Middleware inconsistencies

**Location:** `delivery/middleware/`

**Problem:** `Recoverer()` and `Auth()` write raw JSON with `w.Write()` instead of using `delivery/respond.JSONError()`.

**Fix:** Replace raw writes with `respond.JSONError(w, ...)`.

### B3: Hardcoded `Accept: text/event-stream` header

**Location:** `infrastructure/upstream/client.go`

**Problem:** All chat completions force streaming accept header regardless of client's `stream: false`.

**Fix:** Remove hardcoded header. Upstream returns the format matching the request body's `stream` field.

---

## 5. Testing Strategy

### 5.1 Test Migration

| Current location | New location | Action |
|---|---|---|
| `internal/config/config_test.go` | `internal/config/config_test.go` | Keep |
| `internal/handler/handler_test.go` | `delivery/handler/handler_test.go` | Move + split |
| `internal/metrics/metrics_test.go` | `infrastructure/metrics/counter_test.go` | Move |
| `internal/middleware/middleware_test.go` | `delivery/middleware/middleware_test.go` | Move |
| `internal/model/model_test.go` | `domain/model_test.go` | Move |
| `internal/proxy/normalize_test.go` | `infrastructure/proxy/normalize_test.go` | Move |
| `internal/respond/respond_test.go` | `delivery/respond/respond_test.go` | Move |
| `internal/ringbuffer/ringbuffer_test.go` | `infrastructure/ringbuffer/ringbuffer_test.go` | Move |
| `internal/tor/tor_test.go` | `infrastructure/tor/controller_test.go` | Move |
| `internal/collector/recorder_test.go` | `infrastructure/recorder/recorder_test.go` | Move |
| `internal/translate/*_test.go` | `translate/*_test.go` + `translate/claude/*_test.go` + `translate/gemini/*_test.go` | Move + split |
| `internal/upstream/*_test.go` | `infrastructure/upstream/*_test.go` | Move |

### 5.2 New Tests Required

| Test | Package | Priority |
|---|---|---|
| `ChatService.ProxyChat` integration test (mock upstream) | `application/` | High |
| `ChatService retry loop` — 429 triggers IP rotation | `application/` | High |
| `ChatService retry loop` — body close on retry | `application/` | High |
| `Upstream.OpenCode.ListModels` | `infrastructure/upstream/` | Medium |
| `Refresher` — start/stop/backoff | `infrastructure/upstream/` | Medium |
| `Recorder.sampleLoop` — timeseries sampling | `infrastructure/recorder/` | Medium |
| `Tor Controller` — connection handling (mocked) | `infrastructure/tor/` | Low |
| `Middleware` — uses `respond` package | `delivery/middleware/` | Low |

---

## 6. Migration Order

The refactoring should be done in phases. Each phase must keep tests passing.

### Phase 1: Create new packages (no-op moves)
1. Create `domain/`, `application/`, `server/`, `httputil/`
2. Create `infrastructure/`, `delivery/`, `translate/claude/`, `translate/gemini/`
3. These packages start empty

### Phase 2: Split `httputil` — quick wins
1. Create `httputil/ip.go`, `httputil/header.go`, `httputil/convert.go`
2. Update all callers to use `httputil`
3. Delete duplicated code in middleware, proxy, to_claude, recorder, ui

### Phase 3: Extract domain layer
1. Move `internal/model/model.go` → `domain/model.go` (remove KiloModel, OpenCodeModel)
2. Move `internal/model/request_log.go` → `domain/request_log.go`
3. Move `internal/model/timeseries.go` → `domain/timeseries.go`
4. Create `domain/upstream.go` with `Upstream` interface
5. Create `domain/ip_rotator.go` with `IPRotator` interface
6. Create `infrastructure/upstream/opencode_types.go` + `kilocode_types.go`
7. Update all imports

### Phase 4: Move infrastructure adapters
1. Move `internal/upstream/` → `infrastructure/upstream/`
2. Move `internal/tor/tor.go` → `infrastructure/tor/controller.go`
3. Move `internal/metrics/metrics.go` → `infrastructure/metrics/counter.go`
4. Move `internal/proxy/normalize.go` → `infrastructure/proxy/normalize.go`
5. Move `internal/collector/recorder.go` → `infrastructure/recorder/recorder.go`
6. Move `internal/ringbuffer/` → `infrastructure/ringbuffer/`
7. Update all imports

### Phase 5: Move delivery layer
1. Move `internal/handler/` → `delivery/handler/` (split into files)
2. Move `internal/middleware/` → `delivery/middleware/` (fix respond usage)
3. Move `internal/respond/` → `delivery/respond/`
4. Move `internal/ui/` → `delivery/ui/`
5. Update all imports

### Phase 6: Split translate package
1. Move `internal/translate/` → `translate/` (root files stay)
2. Extract `translate/claude/stream.go` from `to_claude.go`
3. Extract `translate/claude/json.go` from `to_claude.go`
4. Move `translate/claude/request.go` from `claude_request.go`
5. Move `translate/gemini/` from `gemini_request.go` + `to_gemini.go`
6. Delete `to_claude.go`, `claude_request.go`, `to_gemini.go`, `gemini_request.go`

### Phase 7: Create application layer + finish proxy split
1. Create `application/chat.go` — extract ProxyChat orchestration from `internal/proxy/proxy.go`
2. Move remaining HTTP transport helpers from `internal/proxy/proxy.go` → `infrastructure/proxy/client.go`
3. Delete `internal/proxy/proxy.go` (all content distributed to application/ + infrastructure/proxy/)
4. Create `application/models.go` — extract model listing logic
5. Create `application/metrics.go` — wrap metrics counters

### Phase 8: Extract server package
1. Create `server/server.go` — Server struct
2. Reduce `cmd/server/main.go` to thin entry point

### Phase 9: Fix bugs
1. Fix retry loop resource leak
2. Fix middleware JSON responses
3. Fix hardcoded Accept header

### Phase 10: Move tests
1. Move all test files to match new source locations
2. Add new tests from §5.2

---

## 7. Dependency Graph (No Cycles)

```
cmd/server/main.go
    → server/
        → config/
        → application/
            → domain/
            → infrastructure/upstream/
            → infrastructure/tor/
            → infrastructure/proxy/
            → infrastructure/metrics/
            → infrastructure/recorder/
            → infrastructure/ringbuffer/
        → delivery/handler/
            → application/
            → translate/
            → domain/
            → delivery/respond/
        → delivery/middleware/
            → delivery/respond/
        → delivery/ui/
            → infrastructure/recorder/
            → domain/
            → web/
        → httputil/
```

No package imports from a higher layer. `translate/` sits at the same level as `infrastructure/` — it's used by `delivery/handler/` and imports `domain/`.

---

## 8. Excluded from Scope

- **No dependency injection framework.** Go interfaces + manual wiring in `server.New()`.
- **No OpenTelemetry/prometheus.** The existing in-memory metrics are sufficient.
- **No database.** Ring buffers remain in-memory only.
- **No config format changes.** Environment variables remain the sole config source.
- **No API changes.** All HTTP endpoints, response formats, and behavior preserved.

---

## 9. Risks and Mitigations

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| Import cycle during migration | Medium | Follow phase order strictly; run `go build ./...` after each phase |
| Test failures from moved files | Medium | Move tests alongside source files; run full test suite after each phase |
| Missed callers during rename | Low | `grep -r` for old import paths after each move; `go vet ./...` |
| Translate package split breaks streaming | Low | Pure extraction, no logic changes. Run all translate tests |
| ProxyChat refactor changes behavior | Medium | Write `application/chat_test.go` with mock upstream before the refactor |
