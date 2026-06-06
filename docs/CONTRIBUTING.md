# Contributing

Dev setup, scripts, testing, code style, and PR checklist for freegate.

## Prerequisites

- **Go 1.26+** (matches `go.mod`)
- **Tor** (only if running outside docker compose — `apt install tor` or use the `Dockerfile.tor` image)
- **Docker + Docker Compose** (only if using `make up` / `make down`)
- **`make`** (GNU make; standard on Linux/macOS)

Verify:

```bash
go version
docker --version
make --version
```

## Project layout

See `README.md → Project Structure`. Source of truth for new code organization:
- `cmd/server` — entry point
- `internal/application` — use cases (chat, models)
- `internal/delivery` — HTTP-facing layer (handlers, middleware, UI)
- `internal/domain` — types and interfaces that don't depend on frameworks
- `internal/infrastructure` — out-of-process integrations (Tor, upstreams, metrics, recorder)
- `internal/translate` — OpenAI ↔ Claude ↔ Gemini format translation
- `web/` — embedded templates, CSS, JS, fonts

## Available scripts

<!-- AUTO-GENERATED -->

Run `make help` for the full inline list. Targets in the `Makefile`:

| Command | Description |
|---------|-------------|
| `make test` | Run all tests |
| `make test-v` | Run all tests (verbose) |
| `make test-cover` | Run tests with coverage (`coverage.out` + `coverage.html`) |
| `make test-race` | Run tests with the race detector |
| `make test-one name=TestFoo` | Run a single test by name |
| `make build` | Build the server binary → `./server` (CGO=0, stripped) |
| `make run` | Run the server locally (`go run ./cmd/server`) |
| `make vet` | `go vet ./...` |
| `make fmt` | `gofmt -s -w .` |
| `make check` | `fmt vet test` (run before opening a PR) |
| `make tidy` | `go mod tidy` |
| `make up` | `docker compose up -d` |
| `make down` | `docker compose down` |
| `make restart` | `docker compose restart` |
| `make logs svc=proxy` | Tail a service's logs (e.g. `svc=tor`) |
| `make ps` | List running compose services |
| `make ps-all` | List all compose services including stopped |
| `make compose-build` | Build compose service images |
| `make compose-pull` | Pull compose service images |
| `make rebuild svc=proxy` | Rebuild and restart a single service |
| `make clean` | `docker compose down -v` + remove build artifacts |

<!-- /AUTO-GENERATED -->

## Local development workflow

### Without docker

```bash
# 1. Start Tor (system service or a long-running container)
tor &

# 2. Run the proxy against the local Tor SOCKS5
TOR_HOST=127.0.0.1 TOR_CTRL_PORT=9051 LOG_LEVEL=debug make run

# 3. In another shell
curl http://localhost:1234/ready
curl http://localhost:1234/v1/models
```

### With docker compose

```bash
make up            # start proxy + tor
make logs svc=proxy
make ps
make down
```

The compose file binds `127.0.0.1:1234:1234` by default so the dashboard is not exposed to the network.

### Editing templates / static assets

Templates and static files are loaded via `go:embed` (`web/embed.go`). After any change:

- **Templates / static:** restart the server. With `make run` (`go run`) the binary is rebuilt automatically.
- **Go source:** `make run` recompiles; no manual rebuild.

## Testing

`go test ./...` runs the unit suite. Coverage by package:

| Package | Covers |
|---------|--------|
| `internal/config` | env parsing, validation |
| `internal/translate` | format detection (OpenAI/Claude/Gemini), request/response translation, streaming |
| `internal/translate/claude` | Claude ↔ OpenAI JSON, streaming |
| `internal/translate/gemini` | Gemini ↔ OpenAI JSON, streaming |
| `internal/translate/internal/prepost` | thinking normalization, max-tokens adjustment, history sanitization, id/role fixing |
| `internal/infrastructure/proxy` | response normalization, `reasoning_content` collapse, SSE line handling |
| `internal/infrastructure/upstream` | client, model cache, Kilo/OpenCode parsing |
| `internal/infrastructure/tor` | controller (interval, force-rotation, control protocol) |
| `internal/infrastructure/recorder` | ring buffer + timeseries sampler |
| `internal/infrastructure/metrics` | counter / snapshot |
| `internal/infrastructure/ringbuffer` | generic typed ring buffer |
| `internal/delivery/handler` | chat, models, metrics, ready, root, request size limit |
| `internal/delivery/middleware` | auth, CORS, request ID, rate limit |
| `internal/delivery/respond` | JSON + error response helpers |
| `internal/delivery/ui` | dashboard rendering, partials, playground |
| `internal/application` | `ChatService` retry + IP rotation |
| `internal/model` | shared types |
| `internal/httputil` | header copy, client IP extraction, conversion helpers |

Conventions:
- One `_test.go` per source file
- HTTP handler tests use `httptest.NewRecorder` + a stub `ChatProxy` / `ModelLister`
- UI tests load real templates from `web/templates` and assert against rendered HTML

Before opening a PR:

```bash
make check         # fmt + vet + test (no -race, fast)
make test-race     # run with -race if you touched any concurrency code
```

## Code style

- **`gofmt -s`** (run via `make fmt`); CI-equivalent is `make check`
- **`go vet ./...`** (run via `make vet`); must be clean
- **No external linter yet** — `make check` is the gate
- **Imports** — stdlib first, then a blank line, then third-party; do not introduce new third-party deps without a strong reason (current deps: `github.com/go-chi/chi/v5`, `golang.org/x/net/proxy`)
- **Naming** — exported types/functions from `internal/...` are still `PascalCase`; unexported helpers are `camelCase`; tests use `TestXxx` / `t.Run("case", ...)`
- **Errors** — wrap with `fmt.Errorf("...: %w", err)`; never discard with `_` unless the API forces it (and then add a comment)
- **Logging** — `slog` via the default logger set in `internal/server/server.go`; don't introduce `log` or `fmt.Println`
- **Context** — first parameter on anything that does I/O; never store context in a struct
- **Concurrency** — keep shared state behind a mutex; the race detector is part of CI intent (run `make test-race` on changes that touch shared state)

## Architecture rules (enforced informally)

- `domain/` types must not import `infrastructure/`, `delivery/`, or third-party HTTP frameworks
- `application/` depends on `domain/` interfaces only (not on concrete `*upstream.XxxUpstream`)
- `delivery/handler` is the only package that imports `net/http` for handling (middleware does too, but only to wrap handlers)
- New translation directions go in `internal/translate/<format>/`; the hub is OpenAI (see `internal/translate/translate.go` package doc)

## Adding a new upstream

1. Create `internal/infrastructure/upstream/<name>.go` with a type that satisfies the `upstream.Upstream` interface (`Name`, `Start`, `Match`, `ListModels`, `Models`, `ChatCompletion`)
2. Add env vars in `internal/config/config.go` and `.env.example`
3. Wire the new upstream in `internal/server/server.go::New` and the router
4. Add `types.<Name>ModelList` in `internal/infrastructure/upstream/types/` (if not already shared)
5. Add tests alongside the new file
6. Add a column to the dashboard's "Upstreams" card in `web/templates/dashboard.html` and to `internal/delivery/ui/partials.go::buildStatsData`

## Adding a new format translation direction

1. Add the direction under `internal/translate/<format>/` (mirroring `claude/` and `gemini/`)
2. Extend `internal/translate/translate.go::Format` and `Detect` (in `detect.go`) if the new format needs body-based detection
3. Add a `stream_<from>_to_<to>.go` for streaming
4. Add tests using the existing patterns in `claude/*_test.go`

## PR checklist

- [ ] `make check` passes locally (`fmt + vet + test`)
- [ ] `make test-race` passes if concurrency code changed
- [ ] New env vars are added to `internal/config/config.go`, `.env.example`, and `docs/ENV.md`
- [ ] New endpoints are added to `README.md → API Endpoints` and to the dashboard's `# API Endpoints` card
- [ ] Tests cover the new behavior (or the change is a doc-only / refactor)
- [ ] Commit message describes *why*, not just *what*; reference any related issue
- [ ] No debug prints (`fmt.Println`, `log.Println`, `slog.Debug` left in production paths)
- [ ] No new third-party deps without prior discussion

## Security & abuse

This proxy is anonymous-by-design but ships with sensible defaults:

- `API_KEY` defaults to empty (no auth). Set it before exposing past `127.0.0.1`.
- Rate limiter is per-IP, in-memory; it does not survive restart.
- All upstream traffic goes through Tor; don't add direct-connect code paths.
- 429 from an upstream triggers `SIGNAL NEWNYM` (bypasses the 10 s Tor minimum interval — see `internal/infrastructure/tor/controller.go::ForceNewIP`).
- The proxy is a pass-through — it does not persist request bodies, but it does log request IDs, IPs, models, and status codes. Do not log full prompt/response content.

If you find a security issue, please open a private issue rather than a public PR.
