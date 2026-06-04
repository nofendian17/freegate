# Clean Architecture Refactor — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restructure freegate into ports-and-adapters (DDD) layering while fixing bugs, eliminating duplication, and improving test coverage.

**Architecture:** Strict layering — `domain` (innermost) → `application` (use-cases) → `infrastructure` (adapters) → `delivery` (HTTP). Cross-cutting: `config`, `translate`, `server`, `httputil`, `web`. Bug fixes (retry loop leak, middleware JSON inconsistency, hardcoded Accept header) and code dedup (clientIP, copyHeaders, asInt64) are part of the refactor.

**Tech Stack:** Go 1.23, chi router v5, x/net/proxy, golang.org/x/net. No new dependencies.

---

## File Structure Summary

```
freegate/
├── cmd/server/main.go                    ~20 lines after refactor
├── internal/
│   ├── domain/                           NEW — innermost
│   │   ├── model.go                      From internal/model/model.go
│   │   ├── request_log.go                From internal/model/request_log.go
│   │   ├── timeseries.go                 From internal/model/timeseries.go
│   │   ├── upstream.go                   NEW — port interface
│   │   ├── ip_rotator.go                 NEW — port interface
│   │   └── errors.go                     From internal/model/model.go
│   │
│   ├── application/                      NEW — use-case services
│   │   ├── chat.go                       From internal/proxy/proxy.go (ProxyChat)
│   │   ├── models.go                     From internal/upstream/router.go + handler
│   │   └── metrics.go                    Wraps internal/metrics
│   │
│   ├── infrastructure/                   NEW — adapters
│   │   ├── upstream/
│   │   │   ├── router.go                 From internal/upstream/
│   │   │   ├── client.go                 From internal/upstream/
│   │   │   ├── cache.go                  From internal/upstream/
│   │   │   ├── opencode.go               From internal/upstream/
│   │   │   ├── opencode_types.go         NEW — extracted from model
│   │   │   ├── kilocode.go               From internal/upstream/
│   │   │   ├── kilocode_types.go         NEW — extracted from model
│   │   │   └── refresher.go              From internal/upstream/
│   │   ├── tor/controller.go             From internal/tor/
│   │   ├── proxy/
│   │   │   ├── client.go                 From internal/proxy/proxy.go (transport only)
│   │   │   └── normalize.go              From internal/proxy/normalize.go
│   │   ├── metrics/counter.go            From internal/metrics/
│   │   ├── recorder/recorder.go          From internal/collector/
│   │   └── ringbuffer/ringbuffer.go      From internal/ringbuffer/
│   │
│   ├── delivery/                         NEW — HTTP layer
│   │   ├── handler/
│   │   │   ├── handler.go                From internal/handler/ (reduced)
│   │   │   ├── chat.go                   Extracted from handler.go
│   │   │   ├── models.go                 Extracted
│   │   │   ├── ready.go                  Extracted
│   │   │   ├── metrics.go                Extracted
│   │   │   └── root.go                   Extracted
│   │   ├── middleware/middleware.go      From internal/middleware/ (uses respond)
│   │   ├── respond/respond.go            From internal/respond/
│   │   └── ui/                           From internal/ui/
│   │
│   ├── translate/                        NEW — moved up one level
│   │   ├── translate.go                  From internal/translate/
│   │   ├── detect.go                     From internal/translate/
│   │   ├── request.go                    From internal/translate/
│   │   ├── response.go                   From internal/translate/
│   │   ├── writer.go                     Extracted
│   │   ├── claude/
│   │   │   ├── request.go                From internal/translate/claude_request.go
│   │   │   ├── stream.go                 Extracted from to_claude.go
│   │   │   └── json.go                   Extracted from to_claude.go
│   │   └── gemini/
│   │       ├── request.go                From internal/translate/gemini_request.go
│   │       └── stream.go                 From internal/translate/to_gemini.go
│   │
│   ├── server/server.go                  NEW — Server struct
│   │
│   ├── httputil/                         NEW — shared utilities
│   │   ├── ip.go                         Deduplicated ClientIP
│   │   ├── header.go                     Deduplicated CopyHeaders
│   │   └── convert.go                    Deduplicated Int64
│   │
│   └── config/config.go                  Unchanged
│
└── web/                                  Unchanged
```

---

## Phase 0: Pre-flight

### Task 1: Verify baseline tests pass

**Files:** None (read-only verification)

- [ ] **Step 1: Run full test suite**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./... -count=1
```

Expected: All tests pass.

- [ ] **Step 2: Build the project**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./...
```

Expected: No errors.

- [ ] **Step 3: Create a baseline commit point**

```bash
cd /home/beni/Projects/go/lab/freegate && git status
```

If there are uncommitted changes, commit them first with an appropriate message. We want a clean working state before starting.

---

## Phase 1: Create httputil Package (Quick Wins)

The httputil package eliminates 3 duplications: `clientIP()` (middleware.go + proxy.go), `copyHeaders()` (proxy.go + to_claude.go), `asInt64()` (recorder.go + partials.go).

### Task 2: Create httputil/ip.go with ClientIP

**Files:**
- Create: `internal/httputil/ip.go`
- Test: `internal/httputil/ip_test.go`

- [ ] **Step 1: Write the failing test**

```go
package httputil

import (
	"net/http"
	"testing"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name   string
		header string
		remote string
		want   string
	}{
		{"X-Forwarded-For trusted", "203.0.113.1", "10.0.0.1:1234", "203.0.113.1"},
		{"X-Real-IP", "203.0.113.2", "10.0.0.1:1234", "203.0.113.2"},
		{"No forwarded header", "", "10.0.0.1:1234", "10.0.0.1:1234"},
		{"Multiple XFF takes first", "203.0.113.1, 198.51.100.1", "10.0.0.1:1234", "203.0.113.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &http.Request{RemoteAddr: tt.remote, Header: http.Header{}}
			if tt.header != "" {
				r.Header.Set("X-Forwarded-For", tt.header)
			}
			got := ClientIP(r)
			if got != tt.want {
				t.Errorf("ClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/httputil/... -run TestClientIP
```

Expected: FAIL with "package httputil not found" or similar.

- [ ] **Step 3: Create the package and implementation**

Create `internal/httputil/ip.go`:

```go
package httputil

import (
	"net"
	"net/http"
	"strings"
)

func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/httputil/... -run TestClientIP
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/httputil/ip.go internal/httputil/ip_test.go
git commit -m "feat(httputil): add ClientIP helper"
```

### Task 3: Create httputil/header.go with CopyHeaders

**Files:**
- Create: `internal/httputil/header.go`
- Test: `internal/httputil/header_test.go`

- [ ] **Step 1: Write the failing test**

```go
package httputil

import (
	"net/http"
	"testing"
)

func TestCopyHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Set("X-Custom", "value")

	dst := http.Header{}
	CopyHeaders(dst, src)

	if got := dst.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := dst.Get("X-Custom"); got != "value" {
		t.Errorf("X-Custom = %q, want value", got)
	}
}

func TestCopyHeadersSkipsHopByHop(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Set("Connection", "close")
	src.Set("Keep-Alive", "timeout=5")

	dst := http.Header{}
	CopyHeaders(dst, src)

	if dst.Get("Connection") != "" {
		t.Errorf("Connection should be skipped, got %q", dst.Get("Connection"))
	}
	if dst.Get("Keep-Alive") != "" {
		t.Errorf("Keep-Alive should be skipped, got %q", dst.Get("Keep-Alive"))
	}
	if dst.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type should be copied, got %q", dst.Get("Content-Type"))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/httputil/... -run TestCopyHeaders
```

Expected: FAIL

- [ ] **Step 3: Create the implementation**

Create `internal/httputil/header.go`:

```go
package httputil

import "net/http"

var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

func CopyHeaders(dst, src http.Header) {
	for k, v := range src {
		if _, skip := hopByHopHeaders[http.CanonicalHeaderKey(k)]; skip {
			continue
		}
		for _, val := range v {
			dst.Add(k, val)
		}
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/httputil/... -run TestCopyHeaders
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/httputil/header.go internal/httputil/header_test.go
git commit -m "feat(httputil): add CopyHeaders with hop-by-hop filtering"
```

### Task 4: Create httputil/convert.go with Int64

**Files:**
- Create: `internal/httputil/convert.go`
- Test: `internal/httputil/convert_test.go`

- [ ] **Step 1: Write the failing test**

```go
package httputil

import (
	"encoding/json"
	"testing"
)

func TestInt64(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int64
	}{
		{"int", int(42), 42},
		{"int64", int64(42), 42},
		{"int32", int32(42), 42},
		{"float64", float64(42.0), 42},
		{"json.Number", json.Number("42"), 42},
		{"nil", nil, 0},
		{"string", "42", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Int64(tt.in); got != tt.want {
				t.Errorf("Int64(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/httputil/... -run TestInt64
```

Expected: FAIL

- [ ] **Step 3: Create the implementation**

Create `internal/httputil/convert.go`:

```go
package httputil

import (
	"encoding/json"
)

func Int64(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	default:
		return 0
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/httputil/... -run TestInt64
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/httputil/convert.go internal/httputil/convert_test.go
git commit -m "feat(httputil): add Int64 helper for typed any conversion"
```

### Task 5: Update middleware to use httputil.ClientIP

**Files:**
- Modify: `internal/middleware/middleware.go` (replace clientIP function)

- [ ] **Step 1: Remove the local clientIP function and use httputil**

In `internal/middleware/middleware.go`, find the local `clientIP` function and replace it with a call to `httputil.ClientIP`. Add the import if not present.

Replace all callers in this file from `clientIP(r)` to `httputil.ClientIP(r)`, then remove the local function. Add import:
```go
import "freegate/internal/httputil"
```

- [ ] **Step 2: Build to verify**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./...
```

Expected: No errors

- [ ] **Step 3: Run middleware tests**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/middleware/...
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/middleware/middleware.go
git commit -m "refactor(middleware): use httputil.ClientIP"
```

### Task 6: Update proxy to use httputil

**Files:**
- Modify: `internal/proxy/proxy.go` (remove local clientIP + copyHeaders)

- [ ] **Step 1: Replace local helpers in proxy.go**

In `internal/proxy/proxy.go`:
- Remove the local `clientIPFromRequest` function
- Remove the local `copyHeaders` function
- Add import: `"freegate/internal/httputil"`
- Replace callers: `clientIPFromRequest(r)` → `httputil.ClientIP(r)`, `copyHeaders(w, resp.Header)` → `httputil.CopyHeaders(w, resp.Header)`

- [ ] **Step 2: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./internal/proxy/...
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/proxy/proxy.go
git commit -m "refactor(proxy): use httputil for ClientIP and CopyHeaders"
```

### Task 7: Update to_claude.go to use httputil.CopyHeaders

**Files:**
- Modify: `internal/translate/to_claude.go` (remove local copyHeaders)

- [ ] **Step 1: Replace local copyHeaders in to_claude.go**

In `internal/translate/to_claude.go`:
- Remove the local `copyHeaders` function
- Add import: `"freegate/internal/httputil"`
- Replace callers

- [ ] **Step 2: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./internal/translate/...
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/translate/to_claude.go
git commit -m "refactor(translate): use httputil.CopyHeaders in to_claude"
```

### Task 8: Update recorder and ui/partials to use httputil.Int64

**Files:**
- Modify: `internal/collector/recorder.go` (remove local asInt64)
- Modify: `internal/ui/partials.go` (remove local asInt64)

- [ ] **Step 1: Replace asInt64 in recorder.go**

In `internal/collector/recorder.go`:
- Remove the local `asInt64` function
- Add import: `"freegate/internal/httputil"`
- Replace callers: `asInt64(x)` → `httputil.Int64(x)`

- [ ] **Step 2: Replace asInt64 in partials.go**

In `internal/ui/partials.go`:
- Remove the local `asInt64` function
- Add import: `"freegate/internal/httputil"`
- Replace callers

- [ ] **Step 3: Build and test all**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./... -count=1
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/collector/recorder.go internal/ui/partials.go
git commit -m "refactor: use httputil.Int64 in recorder and ui"
```

---

## Phase 2: Create Domain Layer

The domain package has zero internal imports — it's the innermost layer.

### Task 9: Create domain package with model types

**Files:**
- Create: `internal/domain/model.go`
- Create: `internal/domain/errors.go`
- Create: `internal/domain/request_log.go`
- Create: `internal/domain/timeseries.go`
- Create: `internal/domain/upstream.go`
- Create: `internal/domain/ip_rotator.go`

- [ ] **Step 1: Create internal/domain/model.go**

```go
package domain

type Model struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	OwnedBy   string `json:"owned_by"`
	Provider  string `json:"-"`
	Created   int64  `json:"created,omitempty"`
	BadgeURL  string `json:"badge_url,omitempty"`
}

type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

type ErrorResp struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

func NewError(typ, msg, code string) ErrorResp {
	return ErrorResp{Error: ErrorDetail{Type: typ, Message: msg, Code: code}}
}
```

- [ ] **Step 2: Create internal/domain/errors.go**

```go
package domain

import "errors"

var (
	ErrModelNotFound    = errors.New("model not found")
	ErrEmptyRequestBody = errors.New("empty request body")
	ErrBodyTooLarge     = errors.New("request body too large")
)
```

- [ ] **Step 3: Create internal/domain/request_log.go**

```go
package domain

type RequestLogEntry struct {
	Time     string `json:"time"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Status   int    `json:"status"`
	Duration int64  `json:"duration_ms"`
	Tokens   int    `json:"tokens"`
	IP       string `json:"ip"`
	Error    string `json:"error,omitempty"`
}

type RequestLogger func(RequestLogEntry)
```

- [ ] **Step 4: Create internal/domain/timeseries.go**

```go
package domain

type TimeseriesEntry struct {
	Timestamp      int64            `json:"ts"`
	TotalRequests  int              `json:"total_requests"`
	Errors         int              `json:"errors"`
	Retries        int              `json:"retries"`
	RateLimitHits  int              `json:"rate_limit_hits"`
	PerUpstream    map[string]int   `json:"per_upstream"`
}
```

- [ ] **Step 5: Create internal/domain/upstream.go with port interface**

```go
package domain

import (
	"context"
	"net/http"
)

type ChatRequest struct {
	Body        []byte
	OriginalReq *http.Request
}

type Upstream interface {
	Name() string
	Match(modelID string) bool
	ListModels(ctx context.Context) ([]Model, error)
	ChatCompletion(ctx context.Context, req ChatRequest) (*http.Response, error)
	Models() []Model
	Start(ctx context.Context)
}
```

- [ ] **Step 6: Create internal/domain/ip_rotator.go**

```go
package domain

type IPRotator interface {
	NewIP() error
	ForceNewIP() error
	CurrentIP() string
}
```

- [ ] **Step 7: Verify it builds**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./internal/domain/...
```

Expected: No errors

- [ ] **Step 8: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/domain/
git commit -m "feat(domain): add innermost layer with ports and core types"
```

### Task 10: Move upstream-specific types to subpackage

**Files:**
- Create: `internal/upstream/types/opencode.go`
- Create: `internal/upstream/types/kilo.go`
- Modify: `internal/upstream/opencode.go` to use new types
- Modify: `internal/upstream/kilocode.go` to use new types

- [ ] **Step 1: Create the types subpackage**

```bash
cd /home/beni/Projects/go/lab/freegate && mkdir -p internal/upstream/types
```

- [ ] **Step 2: Create internal/upstream/types/opencode.go**

```go
package types

type OpenCodeModelList struct {
	Data []OpenCodeModel `json:"data"`
}

type OpenCodeModel struct {
	ID       string  `json:"id"`
	Object   string  `json:"object"`
	OwnedBy  string  `json:"owned_by"`
	Cost     *string `json:"cost"`
	Display  string  `json:"display_name"`
	BadgeURL string  `json:"badge_url"`
}
```

- [ ] **Step 3: Create internal/upstream/types/kilo.go**

```go
package types

type KiloModelList struct {
	Data []KiloModel `json:"data"`
}

type KiloModel struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Context     *float64          `json:"context_length"`
	TopProvider map[string]any    `json:"top_provider"`
	Pricing     map[string]string `json:"pricing"`
	IsFree      *bool             `json:"is_free"`
}
```

- [ ] **Step 4: Update internal/upstream/opencode.go to reference new types**

In `internal/upstream/opencode.go`:
- Remove local `OpenCodeModel` and `OpenCodeModelList` struct definitions
- Add import: `"freegate/internal/upstream/types"`
- Replace usages: `OpenCodeModel` → `types.OpenCodeModel`, `OpenCodeModelList` → `types.OpenCodeModelList`

- [ ] **Step 5: Update internal/upstream/kilocode.go to reference new types**

Same pattern as Step 4, but for Kilo types.

- [ ] **Step 6: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./internal/upstream/...
```

Expected: PASS

- [ ] **Step 7: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/upstream/
git commit -m "refactor(upstream): move provider-specific types to types subpackage"
```

### Task 11: Update internal/model to use domain types

**Files:**
- Modify: `internal/model/model.go`
- Modify: `internal/model/request_log.go`
- Modify: `internal/model/timeseries.go`

- [ ] **Step 1: Replace internal/model/model.go with type aliases**

```go
package model

import "freegate/internal/domain"

type (
	Model       = domain.Model
	ModelList   = domain.ModelList
	ErrorResp   = domain.ErrorResp
	ErrorDetail = domain.ErrorDetail
)
```

- [ ] **Step 2: Replace internal/model/request_log.go with aliases**

```go
package model

import "freegate/internal/domain"

type (
	RequestLogEntry = domain.RequestLogEntry
	RequestLogger   = domain.RequestLogger
)
```

- [ ] **Step 3: Replace internal/model/timeseries.go with aliases**

```go
package model

import "freegate/internal/domain"

type TimeseriesEntry = domain.TimeseriesEntry
```

- [ ] **Step 4: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/model/
git commit -m "refactor(model): alias to domain types"
```

---

## Phase 3: Move Infrastructure Adapters

### Task 12: Move internal/upstream to infrastructure/upstream

**Files:**
- Move: `internal/upstream/*` → `internal/infrastructure/upstream/*` (except types/)
- Update: all import paths in callers

- [ ] **Step 1: Create new directory and move files**

```bash
cd /home/beni/Projects/go/lab/freegate
mkdir -p internal/infrastructure/upstream
mv internal/upstream/upstream.go internal/infrastructure/upstream/
mv internal/upstream/router.go internal/infrastructure/upstream/
mv internal/upstream/client.go internal/infrastructure/upstream/
mv internal/upstream/cache.go internal/infrastructure/upstream/
mv internal/upstream/opencode.go internal/infrastructure/upstream/
mv internal/upstream/kilocode.go internal/infrastructure/upstream/
mv internal/upstream/refresher.go internal/infrastructure/upstream/
mv internal/upstream/types internal/infrastructure/upstream/types
mv internal/upstream/*_test.go internal/infrastructure/upstream/
rmdir internal/upstream
```

- [ ] **Step 2: Update package declarations in moved files**

All files in `internal/infrastructure/upstream/` keep `package upstream`. The subpackage `internal/infrastructure/upstream/types` keeps `package types`.

- [ ] **Step 3: Find and update all callers**

```bash
cd /home/beni/Projects/go/lab/freegate && grep -rl '"freegate/internal/upstream"' --include='*.go' .
```

For each file found, update the import path from `"freegate/internal/upstream"` to `"freegate/internal/infrastructure/upstream"`. Note: the subpackage `types` is now at `"freegate/internal/infrastructure/upstream/types"`.

- [ ] **Step 4: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor: move upstream package to infrastructure/upstream"
```

### Task 13: Move internal/tor to infrastructure/tor

**Files:**
- Move: `internal/tor/tor.go` → `internal/infrastructure/tor/controller.go`
- Move: `internal/tor/tor_test.go` → `internal/infrastructure/tor/controller_test.go`

- [ ] **Step 1: Move and rename the file**

```bash
cd /home/beni/Projects/go/lab/freegate
mkdir -p internal/infrastructure/tor
mv internal/tor/tor.go internal/infrastructure/tor/controller.go
mv internal/tor/tor_test.go internal/infrastructure/tor/controller_test.go
rmdir internal/tor
```

- [ ] **Step 2: Update package declaration**

In `controller.go`, ensure the package is `package tor`.

- [ ] **Step 3: Find and update callers**

```bash
cd /home/beni/Projects/go/lab/freegate && grep -rl '"freegate/internal/tor"' --include='*.go' .
```

Update imports: `"freegate/internal/tor"` → `"freegate/internal/infrastructure/tor"`.

- [ ] **Step 4: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor: move tor package to infrastructure/tor"
```

### Task 14: Move internal/metrics to infrastructure/metrics

**Files:**
- Move: `internal/metrics/metrics.go` → `internal/infrastructure/metrics/counter.go`
- Move: `internal/metrics/metrics_test.go` → `internal/infrastructure/metrics/counter_test.go`

- [ ] **Step 1: Move and rename files**

```bash
cd /home/beni/Projects/go/lab/freegate
mkdir -p internal/infrastructure/metrics
mv internal/metrics/metrics.go internal/infrastructure/metrics/counter.go
mv internal/metrics/metrics_test.go internal/infrastructure/metrics/counter_test.go
rmdir internal/metrics
```

- [ ] **Step 2: Update package declaration to `package metrics`**

- [ ] **Step 3: Update callers**

```bash
cd /home/beni/Projects/go/lab/freegate && grep -rl '"freegate/internal/metrics"' --include='*.go' .
```

Update imports.

- [ ] **Step 4: Add the missing Incr methods to metrics**

In `internal/infrastructure/metrics/counter.go`, add:

```go
func (m *Metrics) IncrTotal()  { m.TotalRequests.Add(1) }
func (m *Metrics) IncrRetries() { m.Retries.Add(1) }
func (m *Metrics) IncrErrors()  { m.UpstreamErrors.Add(1) }
```

- [ ] **Step 5: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor: move metrics to infrastructure/metrics"
```

### Task 15: Move internal/collector to infrastructure/recorder

**Files:**
- Move: `internal/collector/recorder.go` → `internal/infrastructure/recorder/recorder.go`
- Move: `internal/collector/recorder_test.go` → `internal/infrastructure/recorder/recorder_test.go`

- [ ] **Step 1: Move and rename files**

```bash
cd /home/beni/Projects/go/lab/freegate
mkdir -p internal/infrastructure/recorder
mv internal/collector/recorder.go internal/infrastructure/recorder/recorder.go
mv internal/collector/recorder_test.go internal/infrastructure/recorder/recorder_test.go
rmdir internal/collector
```

- [ ] **Step 2: Update package declaration to `package recorder`**

- [ ] **Step 3: Update callers**

```bash
cd /home/beni/Projects/go/lab/freegate && grep -rl '"freegate/internal/collector"' --include='*.go' .
```

Update imports.

- [ ] **Step 4: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor: move collector to infrastructure/recorder"
```

### Task 16: Move internal/ringbuffer to infrastructure/ringbuffer

**Files:**
- Move: `internal/ringbuffer/*` → `internal/infrastructure/ringbuffer/*`

- [ ] **Step 1: Move files**

```bash
cd /home/beni/Projects/go/lab/freegate
mkdir -p internal/infrastructure/ringbuffer
mv internal/ringbuffer/*.go internal/infrastructure/ringbuffer/
rmdir internal/ringbuffer
```

- [ ] **Step 2: Update callers**

```bash
cd /home/beni/Projects/go/lab/freegate && grep -rl '"freegate/internal/ringbuffer"' --include='*.go' .
```

Update imports.

- [ ] **Step 3: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor: move ringbuffer to infrastructure/ringbuffer"
```

### Task 17: Move internal/proxy/normalize to infrastructure/proxy

**Files:**
- Move: `internal/proxy/normalize.go` → `internal/infrastructure/proxy/normalize.go`
- Move: `internal/proxy/normalize_test.go` → `internal/infrastructure/proxy/normalize_test.go`

- [ ] **Step 1: Move normalize files**

```bash
cd /home/beni/Projects/go/lab/freegate
mkdir -p internal/infrastructure/proxy
mv internal/proxy/normalize.go internal/infrastructure/proxy/normalize.go
mv internal/proxy/normalize_test.go internal/infrastructure/proxy/normalize_test.go
```

- [ ] **Step 2: Update package declaration to `package proxy`**

- [ ] **Step 3: Add NormalizeResponse entry point**

In `internal/infrastructure/proxy/normalize.go`, add at the bottom:

```go
func NormalizeResponse(w http.ResponseWriter, resp *http.Response) error {
	defer resp.Body.Close()
	return copyNormalized(w, resp)
}
```

- [ ] **Step 4: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor: move proxy/normalize to infrastructure/proxy and add entry point"
```

---

## Phase 4: Move Delivery Layer

### Task 18: Move internal/respond to delivery/respond

**Files:**
- Move: `internal/respond/*` → `internal/delivery/respond/*`

- [ ] **Step 1: Move files**

```bash
cd /home/beni/Projects/go/lab/freegate
mkdir -p internal/delivery/respond
mv internal/respond/*.go internal/delivery/respond/
rmdir internal/respond
```

- [ ] **Step 2: Update callers**

```bash
cd /home/beni/Projects/go/lab/freegate && grep -rl '"freegate/internal/respond"' --include='*.go' .
```

Update imports.

- [ ] **Step 3: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor: move respond to delivery/respond"
```

### Task 19: Move internal/middleware to delivery/middleware (with respond fix)

**Files:**
- Move: `internal/middleware/*` → `internal/delivery/middleware/*`

- [ ] **Step 1: Move files**

```bash
cd /home/beni/Projects/go/lab/freegate
mkdir -p internal/delivery/middleware
mv internal/middleware/*.go internal/delivery/middleware/
rmdir internal/middleware
```

- [ ] **Step 2: Update package declaration to `package middleware`**

- [ ] **Step 3: Fix Recoverer and Auth to use respond.JSONError**

In `internal/delivery/middleware/middleware.go`, find the `Recoverer()` function. Replace any manual JSON writes with calls to `respond.JSONError(w, status, msg)`.

Find any panic-recovery JSON write like:
```go
w.WriteHeader(http.StatusInternalServerError)
w.Write([]byte(`{"error":{"message":"internal server error","type":"server_error"}}`))
```

Replace with:
```go
respond.JSONError(w, http.StatusInternalServerError, "internal server error")
```

Add import: `"freegate/internal/delivery/respond"`.

Apply the same fix to `Auth()` for 401/403 responses.

- [ ] **Step 4: Update callers**

```bash
cd /home/beni/Projects/go/lab/freegate && grep -rl '"freegate/internal/middleware"' --include='*.go' .
```

Update imports.

- [ ] **Step 5: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor: move middleware to delivery and use respond package"
```

### Task 20: Move internal/ui to delivery/ui

**Files:**
- Move: `internal/ui/*` → `internal/delivery/ui/*`

- [ ] **Step 1: Move files**

```bash
cd /home/beni/Projects/go/lab/freegate
mkdir -p internal/delivery/ui
mv internal/ui/*.go internal/delivery/ui/
rmdir internal/ui
```

- [ ] **Step 2: Update package declaration to `package ui`**

- [ ] **Step 3: Update callers**

```bash
cd /home/beni/Projects/go/lab/freegate && grep -rl '"freegate/internal/ui"' --include='*.go' .
```

Update imports.

- [ ] **Step 4: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor: move ui to delivery/ui"
```

### Task 21: Move internal/handler to delivery/handler (split into files)

**Files:**
- Move: `internal/handler/handler.go` → `internal/delivery/handler/handler.go` (reduced)
- Create: `internal/delivery/handler/chat.go`
- Create: `internal/delivery/handler/models.go`
- Create: `internal/delivery/handler/ready.go`
- Create: `internal/delivery/handler/metrics.go`
- Create: `internal/delivery/handler/root.go`

- [ ] **Step 1: Create the new package directory**

```bash
cd /home/beni/Projects/go/lab/freegate && mkdir -p internal/delivery/handler
```

- [ ] **Step 2: Move handler.go and strip its handler methods**

```bash
cd /home/beni/Projects/go/lab/freegate && mv internal/handler/handler.go internal/delivery/handler/handler.go
```

In the new `handler.go`, keep only the `Handler` struct, `New()` constructor, and `Routes()` method. Remove the methods that will be moved: `Chat`, `ListModels`, `Ready`, `Metrics`, `Root`.

- [ ] **Step 3: Create chat.go**

```go
package handler

import (
	"io"
	"net/http"
)

func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "could not read body")
		return
	}
	if len(body) == 0 {
		respond.JSONError(w, http.StatusBadRequest, "empty request body")
		return
	}

	// (rest of the Chat method body — extract from old handler.go)
}
```

Move the full Chat method body from the old handler.go.

- [ ] **Step 4: Create models.go, ready.go, metrics.go, root.go**

Move the corresponding methods from the old handler.go into each new file.

- [ ] **Step 5: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 6: Update import path in tests**

```bash
cd /home/beni/Projects/go/lab/freegate
mv internal/handler/handler_test.go internal/delivery/handler/handler_test.go
rmdir internal/handler
```

Update the test file's imports to point to `"freegate/internal/delivery/handler"`.

- [ ] **Step 7: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor: move handler to delivery/handler, split into focused files"
```

---

## Phase 5: Split Translate Package

### Task 22: Move internal/translate root files to internal/translate/

**Files:**
- Create: `internal/translate/` (root files moved here)
- Create: `internal/translate/claude/` and `internal/translate/gemini/`

The existing `internal/translate/` directory will be replaced with a new one. The new directory will host the same files at a higher level, with subdirectories for claude and gemini.

- [ ] **Step 1: Create the new package directory structure**

```bash
cd /home/beni/Projects/go/lab/freegate
mkdir -p internal/translate/claude internal/translate/gemini
```

- [ ] **Step 2: Move root-level translate files**

```bash
cd /home/beni/Projects/go/lab/freegate
mv internal/translate/translate.go internal/translate/
mv internal/translate/detect.go internal/translate/
mv internal/translate/request.go internal/translate/
mv internal/translate/response.go internal/translate/
mv internal/translate/detect_test.go internal/translate/
mv internal/translate/response_test.go internal/translate/
```

- [ ] **Step 3: Move claude_request.go, gemini_request.go, to_gemini.go into subdirs**

```bash
cd /home/beni/Projects/go/lab/freegate
mv internal/translate/claude_request.go internal/translate/claude/request.go
mv internal/translate/claude_request_test.go internal/translate/claude/request_test.go
mv internal/translate/gemini_request.go internal/translate/gemini/request.go
mv internal/translate/gemini_request_test.go internal/translate/gemini/request_test.go
mv internal/translate/to_gemini.go internal/translate/gemini/stream.go
```

- [ ] **Step 4: Update callers**

```bash
cd /home/beni/Projects/go/lab/freegate && grep -rl '"freegate/internal/translate"' --include='*.go' .
```

Update imports. The root path is the same; subpackage paths change to `"freegate/internal/translate/claude"` and `"freegate/internal/translate/gemini"`.

- [ ] **Step 5: Build (to_claude.go still exists with compile errors expected)**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... 2>&1 | head -30
```

There will be errors because `claude_request.go` and `claude_request_test.go` were moved out from the same package as `to_claude.go`. The `to_claude.go` file references functions in `claude_request.go` and vice versa.

- [ ] **Step 6: Commit (intermediate state)**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor(translate): move root files to internal/translate/ and start subpackages"
```

### Task 23: Split to_claude.go into stream.go and json.go

**Files:**
- Create: `internal/translate/claude/stream.go` (extracted from to_claude.go)
- Create: `internal/translate/claude/json.go` (extracted from to_claude.go)
- Delete: `internal/translate/to_claude.go`
- Delete: `internal/translate/to_claude_test.go`

- [ ] **Step 1: Read to_claude.go to identify the split**

```bash
cd /home/beni/Projects/go/lab/freegate && wc -l internal/translate/to_claude.go
```

Read the file. Identify:
- `processOpenAIChunk` and related stream functions → `stream.go`
- `openaiJSONToClaude` and related JSON conversion functions → `json.go`
- `randID` and copyHeaders → move to `stream.go` (or delete copyHeaders if already deduped)
- `claudeStreamState` type → `stream.go`
- `usageInfo` type → `json.go`

- [ ] **Step 2: Create internal/translate/claude/stream.go**

Create the file with all stream-related code from `to_claude.go`. Package declaration: `package claude`. Update function signatures to remove the package prefix where needed.

- [ ] **Step 3: Create internal/translate/claude/json.go**

Create the file with all JSON conversion code from `to_claude.go`. Package declaration: `package claude`.

- [ ] **Step 4: Update package references in claude/request.go**

In `internal/translate/claude/request.go`, update function references to other claude functions. Since they're now in the same package, internal references don't need package prefix.

- [ ] **Step 5: Delete to_claude.go and to_claude_test.go**

```bash
cd /home/beni/Projects/go/lab/freegate && rm internal/translate/to_claude.go internal/translate/to_claude_test.go
```

- [ ] **Step 6: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 7: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor(translate): split 842-line to_claude.go into stream.go and json.go"
```

---

## Phase 6: Create Application Layer

### Task 24: Create application/chat.go with TDD

**Files:**
- Create: `internal/application/chat.go`
- Create: `internal/application/chat_test.go`

- [ ] **Step 1: Write the failing test for ChatService.ProxyChat**

Create `internal/application/chat_test.go`:

```go
package application

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"freegate/internal/domain"
)

type mockRouter struct {
	upstream domain.Upstream
	err      error
}

func (m *mockRouter) Select(modelID string) (domain.Upstream, error) {
	return m.upstream, m.err
}

type mockUpstream struct {
	name      string
	responses []*http.Response
	errors    []error
	calls     int
}

func (m *mockUpstream) Name() string { return m.name }
func (m *mockUpstream) Match(modelID string) bool { return true }
func (m *mockUpstream) ListModels(ctx context.Context) ([]domain.Model, error) {
	return nil, nil
}
func (m *mockUpstream) ChatCompletion(ctx context.Context, req domain.ChatRequest) (*http.Response, error) {
	i := m.calls
	m.calls++
	if i < len(m.errors) && m.errors[i] != nil {
		return nil, m.errors[i]
	}
	return m.responses[i], nil
}
func (m *mockUpstream) Models() []domain.Model { return nil }
func (m *mockUpstream) Start(ctx context.Context) {}

type mockIPRotator struct {
	forceNewIPCalls int
}

func (m *mockIPRotator) NewIP() error { return nil }
func (m *mockIPRotator) ForceNewIP() error {
	m.forceNewIPCalls++
	return nil
}
func (m *mockIPRotator) CurrentIP() string { return "127.0.0.1" }

func TestChatServiceProxyChatSuccess(t *testing.T) {
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     http.Header{},
	}
	upstream := &mockUpstream{name: "test", responses: []*http.Response{resp}}
	router := &mockRouter{upstream: upstream}
	ipRotator := &mockIPRotator{}

	cs := NewChatService(router, ipRotator, nil, 0, 0)
	w := &recordingResponseWriter{header: http.Header{}}
	r := &http.Request{}

	err := cs.ProxyChat(context.Background(), w, r, "test-model", []byte("{}"))
	if err != nil {
		t.Fatalf("ProxyChat failed: %v", err)
	}
	if upstream.calls != 1 {
		t.Errorf("expected 1 upstream call, got %d", upstream.calls)
	}
}

func TestChatServiceProxyChatRetriesOn429(t *testing.T) {
	resp429 := &http.Response{
		StatusCode: 429,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{},
	}
	resp200 := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     http.Header{},
	}
	upstream := &mockUpstream{
		name:      "test",
		responses: []*http.Response{resp429, resp200},
	}
	router := &mockRouter{upstream: upstream}
	ipRotator := &mockIPRotator{}

	cs := NewChatService(router, ipRotator, nil, 1, 10*time.Millisecond)
	w := &recordingResponseWriter{header: http.Header{}}
	r := &http.Request{}

	err := cs.ProxyChat(context.Background(), w, r, "test-model", []byte("{}"))
	if err != nil {
		t.Fatalf("ProxyChat failed: %v", err)
	}
	if upstream.calls != 2 {
		t.Errorf("expected 2 upstream calls, got %d", upstream.calls)
	}
	if ipRotator.forceNewIPCalls != 1 {
		t.Errorf("expected 1 ForceNewIP call, got %d", ipRotator.forceNewIPCalls)
	}
}

func TestChatServiceProxyChatClosesBodyOn429(t *testing.T) {
	closed := false
	body := &closeTracker{ReadCloser: io.NopCloser(strings.NewReader("")), onClose: func() { closed = true }}
	resp429 := &http.Response{StatusCode: 429, Body: body, Header: http.Header{}}
	resp200 := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}
	upstream := &mockUpstream{name: "test", responses: []*http.Response{resp429, resp200}}
	router := &mockRouter{upstream: upstream}
	ipRotator := &mockIPRotator{}

	cs := NewChatService(router, ipRotator, nil, 1, 10*time.Millisecond)
	w := &recordingResponseWriter{header: http.Header{}}
	_ = cs.ProxyChat(context.Background(), w, &http.Request{}, "test-model", []byte("{}"))

	if !closed {
		t.Error("expected 429 response body to be closed before retry")
	}
}

type closeTracker struct {
	io.ReadCloser
	onClose func()
}

func (c *closeTracker) Close() error {
	if c.onClose != nil {
		c.onClose()
	}
	return c.ReadCloser.Close()
}

type recordingResponseWriter struct {
	header http.Header
	body   []byte
	status int
}

func (r *recordingResponseWriter) Header() http.Header { return r.header }
func (r *recordingResponseWriter) Write(b []byte) (int, error) { r.body = append(r.body, b...); return len(b), nil }
func (r *recordingResponseWriter) WriteHeader(s int) { r.status = s }
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/application/... 2>&1 | head -10
```

Expected: FAIL with "package application not found"

- [ ] **Step 3: Create the application package with ChatService**

Create `internal/application/chat.go`:

```go
package application

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"freegate/internal/domain"
	"freegate/internal/httputil"
)

type Router interface {
	Select(modelID string) (domain.Upstream, error)
}

type Metrics interface {
	IncrTotal()
	IncrUpstream(name string)
	IncrRetries()
	IncrErrors()
}

type ChatService struct {
	router     Router
	ipRotator  domain.IPRotator
	metrics    Metrics
	logger     domain.RequestLogger
	maxRetries int
	retryDelay time.Duration
}

func NewChatService(
	router Router,
	ipRotator domain.IPRotator,
	m Metrics,
	maxRetries int,
	retryDelay time.Duration,
) *ChatService {
	return &ChatService{
		router:     router,
		ipRotator:  ipRotator,
		metrics:    m,
		maxRetries: maxRetries,
		retryDelay: retryDelay,
	}
}

func (s *ChatService) ProxyChat(ctx context.Context, w http.ResponseWriter, r *http.Request, modelID string, body []byte) error {
	if s.metrics != nil {
		s.metrics.IncrTotal()
	}

	upstream, err := s.router.Select(modelID)
	if err != nil {
		if s.metrics != nil {
			s.metrics.IncrErrors()
		}
		return err
	}
	if s.metrics != nil {
		s.metrics.IncrUpstream(upstream.Name())
	}

	start := time.Now()

	var resp *http.Response
	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		resp, err = upstream.ChatCompletion(ctx, domain.ChatRequest{Body: body, OriginalReq: r})
		if err != nil {
			if s.metrics != nil {
				s.metrics.IncrErrors()
			}
			return err
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			break
		}
		if s.metrics != nil {
			s.metrics.IncrRetries()
		}
		resp.Body.Close()
		if s.ipRotator != nil {
			_ = s.ipRotator.ForceNewIP()
		}
		if s.retryDelay > 0 {
			time.Sleep(s.retryDelay)
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		if s.metrics != nil {
			s.metrics.IncrErrors()
		}
		return &MaxRetriesExceededError{ModelID: modelID}
	}

	httputil.CopyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if s.logger != nil {
		s.logger(domain.RequestLogEntry{
			Time:     start.Format(time.RFC3339),
			Model:    modelID,
			Provider: upstream.Name(),
			Status:   resp.StatusCode,
			Duration: time.Since(start).Milliseconds(),
		})
	}

	_ = slog.Default
	return nil
}

type MaxRetriesExceededError struct {
	ModelID string
}

func (e *MaxRetriesExceededError) Error() string {
	return "max retries exceeded for model " + e.ModelID
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/application/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/application/
git commit -m "feat(application): add ChatService with retry, IP rotation, and body close"
```

### Task 25: Create application/models.go

**Files:**
- Create: `internal/application/models.go`

- [ ] **Step 1: Create the ModelService**

```go
package application

import (
	"sync"

	"freegate/internal/domain"
)

type RouterRegistry interface {
	AllModels() []domain.Model
	IsReady() bool
}

type ModelService struct {
	mu      sync.RWMutex
	routers []RouterRegistry
}

func NewModelService(routers ...RouterRegistry) *ModelService {
	return &ModelService{routers: routers}
}

func (s *ModelService) AddRouter(r RouterRegistry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routers = append(s.routers, r)
}

func (s *ModelService) AllModels() []domain.Model {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]bool)
	var out []domain.Model
	for _, r := range s.routers {
		for _, m := range r.AllModels() {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	return out
}

func (s *ModelService) IsReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.routers {
		if r.IsReady() {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./internal/application/...
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/application/models.go
git commit -m "feat(application): add ModelService for merged model listing"
```

### Task 26: Wire proxy normalize into ChatService

**Files:**
- Modify: `internal/application/chat.go`

- [ ] **Step 1: Update ChatService.ProxyChat to use proxy.NormalizeResponse**

In `internal/application/chat.go`, replace the `httputil.CopyHeaders + w.WriteHeader` block with:

```go
if err := proxyinfra.NormalizeResponse(w, resp); err != nil {
    slog.Error("normalize response failed", "error", err)
}
```

Add import: `proxyinfra "freegate/internal/infrastructure/proxy"`.

- [ ] **Step 2: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./...
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/application/
git commit -m "feat(application): wire proxy.NormalizeResponse into ChatService"
```

### Task 27: Move remaining internal/proxy/proxy.go transport code to infrastructure/proxy/client.go

**Files:**
- Move: `internal/proxy/proxy.go` (transport helpers) → `internal/infrastructure/proxy/client.go`
- Delete: `internal/proxy/proxy.go`
- Delete: `internal/proxy/` directory

- [ ] **Step 1: Identify what remains in internal/proxy/proxy.go**

After Task 26 wired the normalize call, the proxy.go file should be near-empty or contain only the transport helpers. Move those to `internal/infrastructure/proxy/client.go`.

- [ ] **Step 2: Move transport code to infrastructure/proxy/client.go**

- [ ] **Step 3: Delete internal/proxy/**

```bash
cd /home/beni/Projects/go/lab/freegate && rm -rf internal/proxy
```

- [ ] **Step 4: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./... -count=1
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "refactor: complete proxy split — orchestration in application, transport in infrastructure"
```

---

## Phase 7: Extract Server Package

### Task 28: Create server/server.go

**Files:**
- Create: `internal/server/server.go`
- Modify: `cmd/server/main.go` (reduce to thin entry)

- [ ] **Step 1: Create server/server.go with Server struct**

```go
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"freegate/internal/application"
	"freegate/internal/config"
	"freegate/internal/delivery/handler"
	"freegate/internal/delivery/middleware"
	"freegate/internal/delivery/ui"
	"freegate/internal/infrastructure/metrics"
	"freegate/internal/infrastructure/recorder"
	"freegate/internal/infrastructure/tor"
	"freegate/internal/infrastructure/upstream"
	"freegate/internal/translate"

	"github.com/go-chi/chi/v5"
)

type Server struct {
	cfg     *config.Config
	httpSrv *http.Server
	logger  *slog.Logger
}

func New(cfg *config.Config) (*Server, error) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))

	torCtrl := tor.NewController(cfg.TorHost, cfg.TorCtrlPort, cfg.TorPass, time.Minute*5, logger)

	ocUpstream := upstream.NewOpenCodeUpstream(cfg.UpstreamURLOpenCode, cfg.UpstreamKeyOpenCode, cfg.UpstreamRefreshOpenCode)
	ocUpstream.Start(context.Background())
	kiUpstream := upstream.NewKiloUpstream(cfg.UpstreamURLKilo, cfg.UpstreamKeyKilo, cfg.UpstreamRefreshKilo, cfg.UpstreamKiloPrefixes)
	kiUpstream.Start(context.Background())
	router := upstream.NewRouter(ocUpstream, kiUpstream, cfg.UpstreamDefault)

	rec := recorder.NewRecorder(100, 360)
	rec.SetModelsFunc(router.AllModels)
	rec.SetTorIPFunc(torCtrl.CurrentIP)

	m := metrics.New()

	cs := application.NewChatService(router, torCtrl, m, 2, 3*time.Second)
	ms := application.NewModelService(router)

	templates, err := ui.LoadTemplates()
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	uiHandler := ui.NewHandler(templates, rec, m)
	apiHandler := handler.New(router, cs, ms, m, translate.Default(), logger)

	rateLimiter := middleware.NewRateLimiter(cfg.RateLimit)

	r := chi.NewRouter()
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger(logger))
	r.Use(middleware.Recoverer(logger))
	r.Use(middleware.CORS())
	r.Use(rateLimiter.Middleware())

	r.Get("/", apiHandler.Root)
	r.Get("/ready", apiHandler.Ready)
	r.Mount("/ui/", uiHandler.Routes())

	r.Route("/v1", func(r chi.Router) {
		if cfg.APIKey != "" {
			r.Use(middleware.Auth(cfg.APIKey))
		}
		r.Get("/models", apiHandler.ListModels)
		r.Get("/metrics", apiHandler.Metrics)
		r.Post("/chat/completions", apiHandler.Chat)
	})

	httpSrv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
	}

	go torCtrl.StartMonitor(context.Background())

	return &Server{cfg: cfg, httpSrv: httpSrv, logger: logger}, nil
}

func (s *Server) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutdownCtx)
	}()

	s.logger.Info("server starting", "addr", s.httpSrv.Addr)
	if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
```

(Adjust constructor signatures to match the actual signatures of the underlying types after the refactor.)

- [ ] **Step 2: Reduce cmd/server/main.go to thin entry**

```go
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"freegate/internal/config"
	"freegate/internal/server"
)

func main() {
	cfg := config.Load()
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
```

- [ ] **Step 3: Build and test**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./... && go test ./... -count=1
```

Expected: PASS (after adjusting signatures to match actual constructors)

- [ ] **Step 4: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/server/ cmd/server/
git commit -m "refactor: extract Server struct, reduce main.go to thin entry point"
```

---

## Phase 8: Fix Bugs

### Task 29: Fix hardcoded Accept header in upstream client

**Files:**
- Modify: `internal/infrastructure/upstream/client.go`

- [ ] **Step 1: Add tests for Accept header behavior**

In `internal/infrastructure/upstream/client_test.go`, add:

```go
func TestPostSetsAcceptHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := NewHTTPClient(nil)
	resp, err := client.Post(context.Background(), srv.URL, []byte(`{"stream":true}`), "key")
	if err != nil { t.Fatal(err) }
	defer resp.Body.Close()

	if !strings.Contains(got, "text/event-stream") {
		t.Errorf("expected Accept: text/event-stream for stream=true, got %q", got)
	}
}

func TestPostOmitsAcceptForNonStreaming(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := NewHTTPClient(nil)
	resp, err := client.Post(context.Background(), srv.URL, []byte(`{"stream":false}`), "key")
	if err != nil { t.Fatal(err) }
	defer resp.Body.Close()

	if got == "text/event-stream" {
		t.Errorf("expected no Accept: text/event-stream for stream=false, got %q", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/infrastructure/upstream/... -run TestPost
```

Expected: FAIL (the second test should fail because the header is always set)

- [ ] **Step 3: Implement the conditional Accept header**

In `internal/infrastructure/upstream/client.go`, find `Post` and replace the hardcoded Accept with:

```go
var req struct{ Stream *bool `json:"stream"` }
_ = json.Unmarshal(body, &req)
if req.Stream != nil && *req.Stream {
    req.Header.Set("Accept", "text/event-stream")
}
```

(Adjust based on actual function signature and existing imports.)

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/infrastructure/upstream/... -run TestPost
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add internal/infrastructure/upstream/
git commit -m "fix(upstream): set Accept header based on request stream field, not always"
```

### Task 30: Verify retry loop body close (covered by Task 24 test)

- [ ] **Step 1: Re-run the test**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/application/... -run TestChatServiceProxyChatClosesBodyOn429
```

Expected: PASS

---

## Phase 9: Test Migration and Final Cleanup

### Task 31: Move all test files to match new source locations

- [ ] **Step 1: Verify all test files are in the correct locations**

```bash
cd /home/beni/Projects/go/lab/freegate && find . -name '*_test.go' | sort
```

Compare to expected locations:
- `internal/config/config_test.go`
- `internal/domain/*_test.go`
- `internal/infrastructure/upstream/*_test.go`
- `internal/infrastructure/tor/*_test.go`
- `internal/infrastructure/metrics/*_test.go`
- `internal/infrastructure/recorder/*_test.go`
- `internal/infrastructure/ringbuffer/*_test.go`
- `internal/infrastructure/proxy/*_test.go`
- `internal/application/*_test.go`
- `internal/delivery/handler/*_test.go`
- `internal/delivery/middleware/*_test.go`
- `internal/delivery/respond/*_test.go`
- `internal/delivery/ui/*_test.go`
- `internal/translate/*_test.go` (root)
- `internal/translate/claude/*_test.go`
- `internal/translate/gemini/*_test.go`

- [ ] **Step 2: Move any misplaced test files**

For each misplaced test, use `git mv` (preserves history):

```bash
cd /home/beni/Projects/go/lab/freegate
git mv old/path/test.go new/path/test.go
```

- [ ] **Step 3: Update import paths in test files**

```bash
cd /home/beni/Projects/go/lab/freegate && grep -rl 'freegate/internal/' --include='*_test.go' .
```

Update any import paths that reference old locations.

- [ ] **Step 4: Run all tests**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./... -count=1 -race
```

Expected: All tests pass with no race conditions.

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add -A
git commit -m "test: align test file locations with new package structure"
```

### Task 32: Final verification

- [ ] **Step 1: Full build**

```bash
cd /home/beni/Projects/go/lab/freegate && go build ./...
```

Expected: No errors

- [ ] **Step 2: Full test with race detector**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./... -count=1 -race
```

Expected: All tests pass

- [ ] **Step 3: Vet**

```bash
cd /home/beni/Projects/go/lab/freegate && go vet ./...
```

Expected: No issues

- [ ] **Step 4: Build the production binary**

```bash
cd /home/beni/Projects/go/lab/freegate && go build -o server ./cmd/server
```

Expected: Binary created

- [ ] **Step 5: Update README to reflect new package structure**

Edit `README.md` — update the "Project Structure" tree to match the new layout. Replace the old `internal/` tree with the new one.

- [ ] **Step 6: Commit final changes**

```bash
cd /home/beni/Projects/go/lab/freegate && git add README.md
git commit -m "docs: update README with new package structure"
```

---

## Summary of Commits

After this plan completes, you will have created approximately 30 commits, one per task. Each commit should be a self-contained change with passing tests.

**Verification commands to run throughout:**

- `go build ./...` — verify compilation
- `go test ./... -count=1` — run tests without cache
- `go test ./... -race` — race condition check
- `go vet ./...` — static analysis

**If a task fails:**
1. Read the error message carefully
2. Run `git diff` to see what's been changed
3. Fix the issue (don't move to the next task)
4. Re-run tests
5. Then commit
