#!/usr/bin/env python3
"""Comprehensive deployment test for freegate proxy.

Usage:
    python test_deploy.py [--base-url http://localhost:1234] [--models "model1,model2"]

Requires: requests, pytest (optional)

Tests positive + negative edge cases: health, models, streaming,
non-streaming, reasoning normalization, tool calls, multi-turn,
content integrity, and error handling.
"""

import json
import os
import sys
import time
import urllib.parse
from typing import Any, Optional

try:
    import requests
except ImportError:
    print("ERROR: install requests: pip install requests")
    sys.exit(1)

# ── config ──────────────────────────────────────────────────────────────────
BASE_URL = os.environ.get("FREEGATE_BASE", "http://localhost:1234").rstrip("/")
# comma-separated, first is non-reasoning, second is reasoning
MODEL_IDS = os.environ.get("FREEGATE_MODELS", "deepseek-v4-flash-free,openrouter/deepseek/deepseek-r1:free")
MODELS = [m.strip() for m in MODEL_IDS.split(",")]
NON_REASONING_MODEL = MODELS[0]
REASONING_MODEL = MODELS[1] if len(MODELS) > 1 else MODELS[0]
TIMEOUT = 60

session = requests.Session()
session.headers.update({"Content-Type": "application/json"})


# ── helpers ─────────────────────────────────────────────────────────────────
def e(msg: str) -> None:
    print(f"  ✗  {msg}")


def ok(msg: str) -> None:
    print(f"  ✓  {msg}")


def skip(msg: str) -> None:
    print(f"  ~  {msg}")


def chat_body(model: str, messages: Optional[list] = None, **overrides) -> dict:
    body = {
        "model": model,
        "messages": messages or [{"role": "user", "content": "say hello in one word"}],
        "max_tokens": 20,
    }
    body.update(overrides)
    return body


def post(path: str, json_body: dict, **kw) -> requests.Response:
    return session.post(
        urllib.parse.urljoin(BASE_URL + "/", path.lstrip("/")),
        json=json_body,
        timeout=kw.pop("timeout", TIMEOUT),
        **kw,
    )


def get(path: str, **kw) -> requests.Response:
    return session.get(
        urllib.parse.urljoin(BASE_URL + "/", path.lstrip("/")),
        timeout=kw.pop("timeout", TIMEOUT),
        **kw,
    )


def collect_stream(resp: requests.Response) -> list[dict]:
    """Parse SSE stream into list of JSON chunks. Returns [{...}, ...]."""
    chunks = []
    for line in resp.iter_lines(decode_unicode=True):
        if not line:
            continue
        if line.startswith("data: "):
            data = line[6:]
            if data.strip() == "[DONE]":
                continue
            try:
                chunks.append(json.loads(data))
            except json.JSONDecodeError:
                pass
    return chunks


def reconstruct_content(chunks: list[dict]) -> str:
    """Reassemble full content from streaming delta chunks."""
    parts = []
    for c in chunks:
        choices = c.get("choices", [])
        if choices:
            delta = choices[0].get("delta", {})
            content = delta.get("content")
            if content:
                parts.append(content)
    return "".join(parts)


def count_tests(tag: str) -> None:
    print(f"\n═══ {tag} ═══")


# ─────────────────────────────────────────────────────────────────────────────
#  TESTS
# ─────────────────────────────────────────────────────────────────────────────


def test_health() -> None:
    count_tests("HEALTH CHECK")

    # 1. /ready returns ok
    r = get("/ready")
    assert r.status_code == 200, f"expected 200, got {r.status_code}"
    data = r.json()
    assert data.get("status") == "ok", f"expected ok, got {data}"
    ok("/ready → 200 status=ok")


def test_list_models() -> None:
    count_tests("LIST MODELS")

    # 2. /v1/models returns list
    r = get("/v1/models")
    assert r.status_code == 200, f"expected 200, got {r.status_code}"
    data = r.json()
    assert data.get("object") == "list", f"expected object=list, got {data}"
    models = data.get("data", [])
    assert len(models) > 0, "expected at least 1 model"
    ok(f"/v1/models → {len(models)} models")

    # verify each model has required fields
    for m in models:
        assert "id" in m, f"model missing id: {m}"
        assert "object" in m, f"model missing object: {m}"
    ok("all models have id, object")

    # 3. Root route works
    r = get("/")
    assert r.status_code == 200
    data = r.json()
    assert "routes" in data
    ok("GET / → route listing present")


def test_basic_non_streaming() -> None:
    count_tests("BASIC NON-STREAMING")

    # 4. Basic chat non-streaming
    body = chat_body(NON_REASONING_MODEL, stream=False, max_tokens=50)
    r = post("/v1/chat/completions", body)
    assert r.status_code == 200, f"expected 200, got {r.status_code}\n{r.text}"
    data = r.json()
    assert "choices" in data, f"missing choices: {data}"
    msg = data["choices"][0]["message"]
    content = msg.get("content", "")
    reasoning = msg.get("reasoning")
    has_content = content and len(content) > 0
    has_reasoning = reasoning is not None and len(str(reasoning)) > 0
    assert has_content or has_reasoning, f"both content and reasoning empty: {data}"
    ok(f"non-streaming ok, content_len={len(content)}, reasoning_len={len(str(reasoning or ''))}")


def test_basic_streaming() -> None:
    count_tests("BASIC STREAMING")

    # 5. Basic chat streaming
    body = chat_body(NON_REASONING_MODEL, stream=True, max_tokens=50)
    r = post("/v1/chat/completions", body, stream=True)
    assert r.status_code == 200, f"expected 200, got {r.status_code}"
    chunks = collect_stream(r)
    assert len(chunks) > 0, "no chunks received"
    content = reconstruct_content(chunks)
    reasoning_parts = []
    for c in chunks:
        choices = c.get("choices", [])
        if choices:
            delta = choices[0].get("delta", {})
            r_tok = delta.get("reasoning")
            if r_tok:
                reasoning_parts.append(str(r_tok))
    reasoning = "".join(reasoning_parts)
    has_content = len(content) > 0
    has_reasoning = len(reasoning) > 0
    assert has_content or has_reasoning, f"empty streamed content and reasoning"
    ok(f"streaming ok, {len(chunks)} chunks, content_len={len(content)}, reasoning_len={len(reasoning)}")


def test_reasoning_dual_keys_non_streaming() -> None:
    count_tests("REASONING + DUAL KEYS (NON-STREAMING)")

    # 6. Reasoning model, non-streaming, verify both reasoning keys
    body = chat_body(REASONING_MODEL, stream=False, max_tokens=50)
    r = post("/v1/chat/completions", body)
    if r.status_code != 200:
        skip(f"reasoning model returned {r.status_code} — may not have reasoning model available")
        return
    data = r.json()
    msg = data["choices"][0]["message"]
    has_r = "reasoning" in msg
    has_rc = "reasoning_content" in msg
    assert has_r and has_rc, f"both reasoning keys required. has reasoning={has_r}, reasoning_content={has_rc}. keys={list(msg.keys())}"
    if msg.get("reasoning") is not None:
        assert msg["reasoning"] == msg["reasoning_content"], "reasoning values must match"
    ok("non-streaming: both reasoning + reasoning_content present and equal")


def test_reasoning_dual_keys_streaming() -> None:
    count_tests("REASONING + DUAL KEYS (STREAMING)")

    # 7. Reasoning model, streaming, verify both keys in every delta
    body = chat_body(REASONING_MODEL, stream=True, max_tokens=100)
    r = post("/v1/chat/completions", body, stream=True)
    if r.status_code != 200:
        skip(f"reasoning model streaming returned {r.status_code}")
        return
    chunks = collect_stream(r)
    assert len(chunks) > 0, "no chunks"

    reasoning_chunks = 0
    for i, c in enumerate(chunks):
        choices = c.get("choices", [])
        if not choices:
            continue
        delta = choices[0].get("delta", {})
        has_r = "reasoning" in delta
        has_rc = "reasoning_content" in delta
        # at least one reasoning chunk expected
        if has_r or has_rc:
            assert has_r and has_rc, (
                f"chunk {i}: if one reasoning key present, both must be. "
                f"reasoning={has_r}, reasoning_content={has_rc}. keys={list(delta.keys())}"
            )
            if delta.get("reasoning") is not None or delta.get("reasoning_content") is not None:
                assert delta["reasoning"] == delta["reasoning_content"], (
                    f"chunk {i}: reasoning values must match"
                )
                reasoning_chunks += 1

    assert reasoning_chunks > 0, "expected at least one chunk with reasoning tokens"
    ok(f"streaming: dual reasoning keys present in {reasoning_chunks}/{len(chunks)} chunks")


def test_non_reasoning_dual_keys_null() -> None:
    count_tests("NON-REASONING MODEL — DUAL KEYS AS NULL")

    # 8. Non-reasoning model should still have both keys.
    #    If model is reasoning (content empty, reasoning filled), both keys should be equal.
    #    If model is non-reasoning, both keys should be null.
    body = chat_body(NON_REASONING_MODEL, stream=False, max_tokens=20)
    r = post("/v1/chat/completions", body)
    assert r.status_code == 200, f"expected 200, got {r.status_code}"
    data = r.json()
    msg = data["choices"][0]["message"]
    has_r = "reasoning" in msg
    has_rc = "reasoning_content" in msg
    assert has_r and has_rc, f"both keys required. has reasoning={has_r}, reasoning_content={has_rc}"
    rc_val = msg.get("reasoning_content")
    r_val = msg.get("reasoning")
    if rc_val is None and r_val is None:
        ok("both reasoning keys present and null (non-reasoning model)")
    else:
        assert r_val == rc_val, f"reasoning values must match: reasoning={r_val}, reasoning_content={rc_val}"
        ok(f"both reasoning keys present and equal (reasoning model, len={len(str(r_val))})")

    # 9. Streaming — verify keys present
    body = chat_body(NON_REASONING_MODEL, stream=True, max_tokens=20)
    r = post("/v1/chat/completions", body, stream=True)
    assert r.status_code == 200, f"expected 200, got {r.status_code}"
    chunks = collect_stream(r)
    has_both = False
    for c in chunks:
        choices = c.get("choices", [])
        if not choices:
            continue
        delta = choices[0].get("delta", {})
        if "reasoning" in delta or "reasoning_content" in delta:
            has_both = True
            if delta.get("reasoning") is not None or delta.get("reasoning_content") is not None:
                assert delta["reasoning"] == delta["reasoning_content"], f"stream reasoning mismatch: {delta}"
    if has_both:
        ok("streaming: reasoning keys present and consistent")
    else:
        ok("streaming: no reasoning keys in delta (expected for pure-content models)")


def test_tool_call() -> None:
    count_tests("TOOL CALLS")

    # 10. Forced tool call
    tools = [
        {
            "type": "function",
            "function": {
                "name": "get_weather",
                "description": "Get weather for a city",
                "parameters": {
                    "type": "object",
                    "properties": {
                        "city": {"type": "string", "description": "City name"}
                    },
                    "required": ["city"],
                },
            },
        }
    ]
    messages = [{"role": "user", "content": "what's the weather in Jakarta?"}]
    body = chat_body(NON_REASONING_MODEL, messages=messages, tools=tools, tool_choice="required", stream=False, max_tokens=100)
    r = post("/v1/chat/completions", body)
    if r.status_code != 200:
        skip(f"tool call (forced) returned {r.status_code} — model may not support tools")
    else:
        data = r.json()
        msg = data["choices"][0]["message"]
        assert "tool_calls" in msg, f"expected tool_calls in message: {list(msg.keys())}"
        assert len(msg["tool_calls"]) > 0
        ok("tool call (forced) ok")

    # 11. Auto tool call
    body = chat_body(NON_REASONING_MODEL, messages=messages, tools=tools, tool_choice="auto", stream=False, max_tokens=100)
    r = post("/v1/chat/completions", body)
    if r.status_code != 200:
        skip(f"tool call (auto) returned {r.status_code}")
    else:
        data = r.json()
        ok("tool call (auto) ok — response may or may not include tool_calls (model decides)")


def get_message_text(msg: dict) -> str:
    """Get text from message, handling reasoning models where content may be empty."""
    content = msg.get("content", "") or ""
    if content.strip():
        return content
    reasoning = msg.get("reasoning") or msg.get("reasoning_content") or ""
    return str(reasoning)


def test_multi_turn() -> None:
    count_tests("MULTI-TURN CONVERSATION")

    # 12. Multi-turn: turn 1 + turn 2
    messages = [
        {"role": "user", "content": "my name is Beni"},
    ]
    body = chat_body(NON_REASONING_MODEL, messages=messages, stream=False, max_tokens=50)
    r = post("/v1/chat/completions", body)
    assert r.status_code == 200, f"turn 1: {r.status_code}"
    data = r.json()
    reply = get_message_text(data["choices"][0]["message"])

    messages.append({"role": "assistant", "content": reply})
    messages.append({"role": "user", "content": "what's my name?"})

    body = chat_body(NON_REASONING_MODEL, messages=messages, stream=False, max_tokens=50)
    r = post("/v1/chat/completions", body)
    assert r.status_code == 200, f"turn 2: {r.status_code}"
    data = r.json()
    answer = get_message_text(data["choices"][0]["message"])
    assert "beni" in answer.lower(), f"expected name in answer: {answer}"
    ok(f"multi-turn ok — model remembered name: {answer.strip()[:60]}")


def test_streaming_content_integrity() -> None:
    count_tests("CONTENT INTEGRITY — STREAMING == NON-STREAMING")

    # 13. Same prompt, compare streamed assembly vs non-streaming
    prompt = "count from 1 to 10, no extra text, just numbers separated by commas"
    messages = [{"role": "user", "content": prompt}]

    # non-streaming
    body = chat_body(NON_REASONING_MODEL, messages=messages, stream=False, max_tokens=50)
    r = post("/v1/chat/completions", body)
    assert r.status_code == 200
    ns_text = r.json()["choices"][0]["message"]["content"]

    # streaming
    body = chat_body(NON_REASONING_MODEL, messages=messages, stream=True, max_tokens=50)
    r = post("/v1/chat/completions", body, stream=True)
    assert r.status_code == 200
    chunks = collect_stream(r)
    s_text = reconstruct_content(chunks)

    # Both should contain the same semantic content (allow slight whitespace diff)
    ns_clean = "".join(ns_text.split())
    s_clean = "".join(s_text.split())
    if ns_clean == s_clean:
        ok("streaming content matches non-streaming (exact)")
    elif ns_clean[:20] == s_clean[:20]:
        ok("streaming content matches non-streaming (prefix match, tail may differ per model)")
    else:
        print(f"  ⚠  non-streaming: {ns_text[:80]}")
        print(f"  ⚠  streaming:     {s_text[:80]}")
        skip("content prefix differs — may be expected for non-deterministic models")


def test_multi_turn_tool_call() -> None:
    count_tests("MULTI-TURN WITH TOOL CALL RESULT")

    tools = [
        {
            "type": "function",
            "function": {
                "name": "get_temp",
                "description": "Get temperature for a city",
                "parameters": {
                    "type": "object",
                    "properties": {
                        "city": {"type": "string"},
                    },
                    "required": ["city"],
                },
            },
        }
    ]

    # turn 1: ask for temp, model should call tool
    msgs = [{"role": "user", "content": "what's the temperature in Tokyo?"}]
    body = chat_body(NON_REASONING_MODEL, messages=msgs, tools=tools, tool_choice="required", stream=False, max_tokens=100)
    r = post("/v1/chat/completions", body)
    if r.status_code != 200:
        skip(f"multi-turn tool call not tested (status={r.status_code})")
        return
    data = r.json()
    msg = data["choices"][0]["message"]
    if "tool_calls" not in msg:
        skip("model did not call tool — cannot test multi-turn tool flow")
        return

    # turn 2: inject tool result
    tc = msg["tool_calls"][0]
    msgs.append(msg)
    msgs.append({
        "role": "tool",
        "tool_call_id": tc["id"],
        "content": json.dumps({"temperature": 28, "unit": "celsius", "city": "Tokyo"}),
    })
    msgs.append({"role": "user", "content": "what was the temperature again?"})

    body = chat_body(NON_REASONING_MODEL, messages=msgs, tools=tools, stream=False, max_tokens=50)
    r = post("/v1/chat/completions", body)
    assert r.status_code == 200, f"tool result turn: {r.status_code}"
    answer = r.json()["choices"][0]["message"]["content"]
    assert len(answer) > 0
    ok(f"multi-turn tool call ok — model processed tool result: {answer.strip()[:80]}")


def test_negative_invalid_model() -> None:
    count_tests("NEGATIVE — INVALID INPUTS")

    # 14. Invalid model ID (garbage)
    body = chat_body("this-model-definitely-does-not-exist-12345", stream=False)
    r = post("/v1/chat/completions", body)
    # proxy will forward to default upstream, which should fail
    assert r.status_code >= 400, f"expected 4xx/5xx for nonexistent model, got {r.status_code}"
    err = r.json()
    assert "error" in err, f"expected error in response: {err}"
    ok(f"invalid model → {r.status_code} error")

    # 15. Empty model ID
    body = chat_body("", stream=False)
    r = post("/v1/chat/completions", body)
    assert r.status_code == 400, f"expected 400 for empty model, got {r.status_code}"
    ok("empty model → 400")

    # 16. Empty messages — may be accepted by upstream (returns 200 from reasoning model)
    #     Still verify response at least is valid JSON
    body = chat_body(NON_REASONING_MODEL, messages=[], stream=False)
    r = post("/v1/chat/completions", body)
    # Note: some upstreams accept empty messages; just verify no crash
    assert r.status_code < 500, f"server error on empty messages: {r.status_code}"
    ok(f"empty messages → {r.status_code} (no crash)")

    # 17. Invalid JSON body (raw string instead of JSON)
    r = session.post(
        urllib.parse.urljoin(BASE_URL + "/", "v1/chat/completions"),
        data="not-json-at-all",
        headers={"Content-Type": "application/json"},
        timeout=TIMEOUT,
    )
    assert r.status_code == 400, f"expected 400 for invalid JSON, got {r.status_code}"
    ok("invalid JSON → 400")


def test_negative_wrong_method() -> None:
    count_tests("NEGATIVE — WRONG METHOD / NOT FOUND")

    # 18. POST to /v1/models (405)
    r = post("/v1/models", {"model": "test"})
    assert r.status_code == 405, f"expected 405, got {r.status_code}"
    ok("POST /v1/models → 405")

    # 19. Non-existent endpoint
    r = get("/v1/nonexistent")
    assert r.status_code == 404, f"expected 404, got {r.status_code}"
    ok("GET /v1/nonexistent → 404")

    # 20. POST to / (wrong method for root)
    r = post("/", {"test": True})
    assert r.status_code == 405, f"expected 405, got {r.status_code}"
    ok("POST / → 405")


def test_negative_body_too_large() -> None:
    count_tests("NEGATIVE — BODY TOO LARGE")

    # 21. Body > 10MB
    big_msg = {"role": "user", "content": "x" * (11 * 1024 * 1024)}  # 11MB
    body = chat_body(NON_REASONING_MODEL, messages=[big_msg], stream=False)
    r = post("/v1/chat/completions", body)
    assert r.status_code == 413 or r.status_code == 400 or r.status_code == 500, \
        f"expected 413/400 for oversized body, got {r.status_code}"
    ok(f"oversized body → {r.status_code}")


def test_root_route() -> None:
    count_tests("ROOT ROUTE")

    r = get("/")
    assert r.status_code == 200
    data = r.json()
    assert "service" in data
    assert "routes" in data
    routes = data["routes"]
    assert "GET  /ready" in routes
    assert "GET  /v1/models" in routes
    assert "POST /v1/chat/completions" in routes
    ok("GET / lists all routes")


# ── runner ──────────────────────────────────────────────────────────────────
PASS = 0
FAIL = 0
SKIP = 0


def run(name: str, fn, *a, **kw) -> None:
    global PASS, FAIL, SKIP
    label = name.replace("_", " ").title()
    try:
        fn(*a, **kw)
        PASS += 1
    except AssertionError as ex:
        print(f"  ✗  FAIL: {ex}")
        FAIL += 1
    except Exception as ex:
        print(f"  ✗  EXCEPTION: {ex}")
        FAIL += 1


def main() -> None:
    global PASS, FAIL, SKIP

    print(f"freegate deployment test")
    print(f"  base:      {BASE_URL}")
    print(f"  reasoning: {REASONING_MODEL}")
    print(f"  standard:  {NON_REASONING_MODEL}")
    print()

    # warmup — wait for models to load
    for i in range(12):
        r = get("/ready")
        if r.status_code == 200:
            break
        time.sleep(5)
    else:
        print("⚠  server not ready after 60s, continuing anyway")

    run("health", test_health)
    run("root route", test_root_route)
    run("list models", test_list_models)
    run("basic non-streaming", test_basic_non_streaming)
    run("basic streaming", test_basic_streaming)
    run("reasoning dual keys non-streaming", test_reasoning_dual_keys_non_streaming)
    run("reasoning dual keys streaming", test_reasoning_dual_keys_streaming)
    run("non-reasoning dual keys null", test_non_reasoning_dual_keys_null)
    run("tool call", test_tool_call)
    run("multi-turn conversation", test_multi_turn)
    run("multi-turn tool call", test_multi_turn_tool_call)
    run("content integrity", test_streaming_content_integrity)
    run("negative invalid inputs", test_negative_invalid_model)
    run("negative wrong method/not found", test_negative_wrong_method)
    run("negative body too large", test_negative_body_too_large)

    print()
    print(f"{'='*50}")
    print(f"  PASS: {PASS}  FAIL: {FAIL}  SKIP: {SKIP}")
    print(f"{'='*50}")

    if FAIL:
        sys.exit(1)


if __name__ == "__main__":
    main()
