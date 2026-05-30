# freegate

Multi-upstream OpenAI-compatible API proxy for free AI models, routed through Tor.

freegate proxies `/v1/chat/completions` and `/v1/models` requests to **opencode.ai** and **kilo.ai** (OpenRouter), automatically routing by model ID prefix. All traffic goes through Tor SOCKS5 for anonymity. Only free models are served. Streaming responses include dual reasoning fields (`reasoning` + `reasoning_content`) for compatibility with both OpenCode and OpenRouter/Kilo clients.

## Features

- **Multi-upstream routing** — model prefix determines the upstream: `kilo/`, `kilo-`, `openrouter/` → Kilo; prefixless → OpenCode
- **Free only** — automatically filters out paid models (`isFree == true` for Kilo, `cost == "0"` for OpenCode); merged & deduped on `/v1/models`
- **Tor by default** — all upstream traffic through Tor SOCKS5 (`:9050`); 429 retries rotate Tor IP
- **Reasoning normalization** — every response (streaming + non-streaming) includes both `reasoning` and `reasoning_content` fields, regardless of upstream format
- **Rate limiting** — per-IP rate limiter, configurable via env
- **Optional auth** — API key validation via `Authorization: Bearer <key>` header
- **Docker Compose** — single command to start both proxy and Tor

## Quick Start

```bash
docker compose up -d
```

The proxy will be available at `http://localhost:1234`.

## Usage

```bash
# List available free models
curl http://localhost:1234/v1/models

# Chat completion (streaming)
curl -X POST http://localhost:1234/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"openrouter/owl-alpha","messages":[{"role":"user","content":"hello"}],"max_tokens":50}'

# Chat completion (non-streaming)
curl -X POST http://localhost:1234/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hello"}],"stream":false}'

# Health check
curl http://localhost:1234/ready
```

## Routing Rules

| Model ID pattern | Upstream | Example |
|-----------------|----------|---------|
| `kilo/...`, `kilo-...` | Kilo (OpenRouter) | `kilo-auto/free` |
| `openrouter/...` | Kilo (OpenRouter) | `openrouter/owl-alpha` |
| `nvidia/...` | Kilo (OpenRouter) | `nvidia/nemotron-3:free` |
| `poolside/...` | Kilo (OpenRouter) | `poolside/laguna-m.1:free` |
| Default (no prefix match) | OpenCode.ai | `deepseek-v4-flash-free` |

Models with `:free` suffix are also routed to Kilo.

## Configuration

All settings are environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `1234` | Server port |
| `TOR_HOST` | `127.0.0.1` | Tor SOCKS host |
| `TOR_PORT` | `9050` | Tor SOCKS port |
| `TOR_CTRL_PORT` | `9051` | Tor control port |
| `TOR_PASS` | (empty) | Tor control password |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `API_KEY` | (empty) | Optional auth key; empty = no auth |
| `RATE_LIMIT` | `60` | Requests per minute per IP |
| `UPSTREAM_URL_OPENCODE` | `https://opencode.ai/zen/v1` | OpenCode upstream URL |
| `UPSTREAM_KEY_OPENCODE` | `public` | OpenCode API key |
| `UPSTREAM_URL_KILO` | `https://api.kilo.ai/api/openrouter` | Kilo upstream URL |
| `UPSTREAM_KEY_KILO` | `anonymous` | Kilo API key |
| `UPSTREAM_DEFAULT` | `opencode` | Default upstream for unmatched models |
| `UPSTREAM_KILO_PREFIXES` | `kilo/,kilo-,openrouter/` | Comma-separated prefix list for Kilo routing |

## Reasoning Normalization

OpenCode uses `reasoning_content` for reasoning tokens; OpenRouter/Kilo use `reasoning`. freegate normalizes so **both fields** appear in every response:

```json
{
  "choices": [{
    "message": {
      "content": "Final answer here",
      "reasoning": "Step-by-step thought process...",
      "reasoning_content": "Step-by-step thought process..."
    }
  }]
}
```

This applies to both streaming (`delta`) and non-streaming (`message`) responses.

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/v1/models` | List all free models from all upstreams (merged, deduped) |
| `POST` | `/v1/chat/completions` | OpenAI-compatible chat completions |
| `GET` | `/ready` | Health check |

## Architecture

```
┌──────────┐     ┌──────────────┐     ┌────────────┐     ┌──────────────────┐
│  Client   │────▶│  freegate     │────▶│ Tor SOCKS5 │────▶│  Upstream         │
│           │     │  (:1234)      │     │  :9050     │     │                  │
│ CLI/IDE   │     │  Router       │     └────────────┘     ├─ opencode.ai     │
│ curl      │     │  ├─ kilo/   ──┼────────────────────────┤  /zen/v1         │
│           │     │  └─ default──┼────────────────────────┤  key: public     │
│           │     │              │     ┌────────────┐     ├─ api.kilo.ai     │
│           │     │  Models      │────▶│ Tor SOCKS5 │────▶│  /api/openrouter │
│           │     │  (free only) │     │  :9050     │     │  key: anonymous  │
└──────────┘     └──────────────┘     └────────────┘     └──────────────────┘
```

## Development

```bash
# Build
go build -o server ./cmd/server

# Test
go test ./... -count=1

# Build Docker
docker compose build
```

## Tech Stack

- **Go 1.23+** — core proxy server
- **[chi](https://github.com/go-chi/chi/v5)** — HTTP router
- **[Tor](https://www.torproject.org/)** — SOCKS5 proxy + IP rotation on 429
- **Docker Compose** — orchestration
