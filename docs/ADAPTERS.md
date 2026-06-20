# Adapters

How to use freegate as the API backend for Claude Code and Codex CLI.

## Prerequisites

- freegate running and accessible (default: `http://localhost:1234`)
- Models loaded (check `http://localhost:1234/ready` returns 200, or visit the dashboard at `http://localhost:1234/`)
- If `API_KEY` is set, note the key for authentication

---

## Claude Code

freegate exposes `/v1/messages` which accepts the Claude Messages API format and translates it to the upstream OpenAI format. Point Claude Code's `ANTHROPIC_BASE_URL` at freegate to route all requests through it.

### Configuration

```bash
# Base URL — no /v1 suffix, freegate handles routing
export ANTHROPIC_BASE_URL="http://localhost:1234"

# Auth token — required if freegate has API_KEY set; omit if empty
export ANTHROPIC_AUTH_TOKEN="your-api-key"

# Model mapping — tell Claude Code which free model to use for each tier
export ANTHROPIC_DEFAULT_SONNET_MODEL="deepseek-v4-flash-free"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="deepseek-v4-flash-free"
export ANTHROPIC_DEFAULT_OPUS_MODEL="deepseek-v4-flash-free"
export ANTHROPIC_DEFAULT_FABLE_MODEL="deepseek-v4-flash-free"

# Enable model discovery from freegate's /v1/models (Claude Code v2.1.129+)
export CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1
```

Models discovered from the gateway appear in the `/model` picker labelled "From gateway".

### Per-project settings

Add to `.claude/settings.local.json`:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://localhost:1234",
    "ANTHROPIC_AUTH_TOKEN": "your-api-key",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek-v4-flash-free",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek-v4-flash-free",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "deepseek-v4-flash-free",
    "ANTHROPIC_DEFAULT_FABLE_MODEL": "deepseek-v4-flash-free",
    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"
  }
}
```

### Finding available models

List all models freegate exposes:

```bash
curl http://localhost:1234/v1/models | jq -r '.data[].id' | sort
```

Use any of these model IDs in `ANTHROPIC_DEFAULT_*_MODEL`. Tip: pick a model that is routed to the upstream you prefer. Use `deepseek-v4-flash-free` (OpenCode) or `openrouter/owl-alpha` (Kilo).

### How it works

```
Claude Code  ──POST /v1/messages──►  freegate  ──translate──►  upstream OpenAI API
     │                                      │
     │          ◄──response (Claude format) ──┘
     │                                      │
     └──GET /v1/models──────────────────────┘ (discovery)
```

freegate detects the Claude format by structural hints (`anthropic_version`, `max_tokens`, `system`, tool_use/image blocks) and translates the request to OpenAI format before forwarding to the upstream. The response is translated back to Claude format before returning to Claude Code. Streaming works for both SSE and non-streaming requests.

---

## Codex CLI

freegate exposes `/v1/chat/completions` which is fully OpenAI-compatible. Point Codex CLI's `openai_base_url` or a custom provider at freegate.

### Quick start — single command

Use `-c` / `--config` to override the base URL for a single run:

```bash
codex -c openai_base_url='"http://localhost:1234/v1"' -m deepseek-v4-flash-free
```

### Persistent configuration

Add to `~/.codex/config.toml`:

```toml
openai_base_url = "http://localhost:1234/v1"
model = "deepseek-v4-flash-free"
```

This changes the base URL for the built-in OpenAI provider. If freegate has `API_KEY` set, pass it as an env var on each run:

```bash
OPENAI_API_KEY=your-key codex
```

Or define a custom provider:

```toml
model = "deepseek-v4-flash-free"
model_provider = "freegate"

[model_providers.freegate]
name = "freegate"
base_url = "http://localhost:1234/v1"
env_key = "OPENAI_API_KEY"
```

Using a custom provider lets you keep the built-in OpenAI provider configured separately.

### Per-project configuration

Add to `.codex/config.toml` in your repo:

```toml
model = "deepseek-v4-flash-free"
```

Project configs cannot set `openai_base_url` or `model_providers` for security reasons — those must go in `~/.codex/config.toml`. The project-level `model` setting is enough to select your freegate model once the base URL is configured at the user level.

### CLI profiles

Create a dedicated profile for freegate:

```toml
# ~/.codex/freegate.config.toml
model = "deepseek-v4-flash-free"
openai_base_url = "http://localhost:1234/v1"
```

Then use it:

```bash
codex --profile freegate
codex exec --profile freegate "review this diff"
```

### Finding available models

```bash
curl http://localhost:1234/v1/models | jq -r '.data[].id' | sort
```

Pass any model ID with `-m` / `--model` or set it in `model` in config.

### How it works

```
Codex CLI  ──POST /v1/chat/completions──►  freegate  ──►  upstream
     │
     └──GET /v1/models──────────────────────┘
```

Codex CLI sends OpenAI Chat Completions format; freegate proxies it directly to the selected upstream without translation. Streaming, reasoning, and tool calls all pass through natively.

---

## Authentication

If freegate is configured with `API_KEY`, both Claude Code and Codex CLI must include it:

| Tool | How to pass |
|------|-------------|
| **Claude Code** | `ANTHROPIC_AUTH_TOKEN` env var |
| **Codex CLI** | `OPENAI_API_KEY` env var (or the `env_key` configured in `[model_providers]`) |

If `API_KEY` is empty (default), no auth is needed. The dashboard at `/` is always open regardless of auth.

---

## Troubleshooting

**"No models available" / 503 on /ready**
The upstream catalogs haven't loaded yet. Wait a few seconds and check the dashboard at `http://localhost:1234/`.

**"401 Unauthorized"**
freegate has `API_KEY` set but the tool isn't sending it. Set the auth env var (see table above).

**Claude Code shows "model not found"**
Model discovery is off by default (`CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY`). Either enable it or set `ANTHROPIC_DEFAULT_*_MODEL` to a model ID from `GET /v1/models`.

**Codex CLI doesn't detect freegate models**
`openai_base_url` changes where requests go but does not auto-discover models. Set `model` explicitly in config or use `-m`.

**Rate limited (429)**
freegate's default rate limit is 60 req/min per IP. Check `RATE_LIMIT` env var. Tor IP rotation retries 429s from upstreams automatically.

**Slow first request**
Tor circuit establishment adds latency on the first request. Subsequent requests reuse the circuit.
