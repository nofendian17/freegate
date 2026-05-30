import json, http.client, sys, time

BASE = "oc.onehub.biz.id"
UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"
TO = 45
DELAY = 2
passed = 0
failed = 0
skipped = []

def delta(c):
    choices = c.get('choices')
    if not choices or len(choices) == 0:
        return None
    return choices[0].get('delta', {})

def chat(body):
    is_stream = body.get("stream", False)
    time.sleep(DELAY)
    try:
        conn = http.client.HTTPSConnection(BASE, timeout=TO)
        conn.request("POST", "/v1/chat/completions", json.dumps(body), {
            "Content-Type": "application/json", "User-Agent": UA,
            "Accept": "text/event-stream" if is_stream else "application/json"
        })
        resp = conn.getresponse()
        data = resp.read().decode()
        conn.close()
        return data
    except Exception as e:
        return json.dumps({"error":{"message":str(e)}})

def get(path):
    time.sleep(DELAY)
    conn = http.client.HTTPSConnection(BASE, timeout=10)
    conn.request("GET", path, headers={"User-Agent": UA})
    resp = conn.getresponse()
    data = resp.read().decode()
    conn.close()
    return resp.status, data

def test(name, ok, detail=""):
    global passed, failed
    if "rate limit" in detail.lower() or "try again" in detail.lower():
        skipped.append(name)
        print(f"  ⏭️ {name} (rate limited)")
        return
    if ok:
        passed += 1
        print(f"  ✅ {name}")
    else:
        failed += 1
        print(f"  ❌ {name} — {detail}")


# ═══════════════════════════════════════════════
# POSITIVE CASES
# ═══════════════════════════════════════════════

print("=" * 50)
print("POSITIVE CASES")
print("=" * 50)

s, b = get("/ready")
test("P1: /ready returns 200", s == 200)

s, b = get("/v1/models")
models = json.loads(b).get('data', []) if s == 200 else []
test("P2: /v1/models returns 200+", s == 200)
test("P2b: has multiple models", len(models) >= 2)
test("P2c: has kilo model", any(m.get('provider')=='kilo' for m in models))
test("P2d: has opencode model", any(m.get('provider')=='opencode' for m in models))

b = json.loads(chat({"model":"openrouter/owl-alpha","messages":[{"role":"user","content":"say hi"}],"max_tokens":10}))
m = b['choices'][0]['message']
test("P3: non-streaming returns content", bool(m.get('content','').strip()))
test("P3b: has reasoning key", 'reasoning' in m)
test("P3c: has reasoning_content key", 'reasoning_content' in m)

b = json.loads(chat({"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"What is 2+2? Think."}],"max_tokens":100}))
m = b['choices'][0]['message']
r = m.get('reasoning','') or ''
rc = m.get('reasoning_content','') or ''
test("P4: reasoning has content", len(r) > 0)
test("P4b: reasoning == reasoning_content", r == rc)
test("P4c: finish_reason present", bool(b['choices'][0].get('finish_reason')))

raw = chat({"model":"openrouter/owl-alpha","messages":[{"role":"user","content":"say hi"}],"max_tokens":10,"stream":True})
lines = [l for l in raw.split('\n') if l.startswith('data: ') and l.strip() != 'data: [DONE]']
test("P5: streaming has chunks", len(lines) > 0)
d = delta(json.loads(lines[0][6:])) if lines else {}
test("P5b: first chunk has reasoning", 'reasoning' in d if d else False)
test("P5c: first chunk has reasoning_content", 'reasoning_content' in d if d else False)
all_dual = all('reasoning' in delta(json.loads(l[6:])) and 'reasoning_content' in delta(json.loads(l[6:])) for l in lines if delta(json.loads(l[6:])))
test("P5d: ALL chunks dual_keys", all_dual)

raw = chat({"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"Think: 2+2?"}],"max_tokens":50,"stream":True})
lines = [l for l in raw.split('\n') if l.startswith('data: ') and l.strip() != 'data: [DONE]']
test("P6: reasoning streaming has chunks", len(lines) > 0)
all_dual = all('reasoning' in delta(json.loads(l[6:])) and 'reasoning_content' in delta(json.loads(l[6:])) for l in lines if delta(json.loads(l[6:])))
test("P6b: ALL chunks dual_keys", all_dual)
r_all = ""
rc_all = ""
for l in lines:
    d = delta(json.loads(l[6:]))
    if d:
        r_all += d.get('reasoning','') or ''
        rc_all += d.get('reasoning_content','') or ''
if r_all or rc_all:
    test("P6c: reasoning == reasoning_content", r_all.strip() == rc_all.strip())

# P7: deterministic prompt for content integrity
ns = json.loads(chat({"model":"openrouter/owl-alpha","messages":[{"role":"user","content":"Reply with exactly one word: OK"}],"max_tokens":5}))
ns_c = (ns.get('choices',[{}])[0].get('message',{}).get('content','') or '').strip()
raw = chat({"model":"openrouter/owl-alpha","messages":[{"role":"user","content":"Reply with exactly one word: OK"}],"max_tokens":5,"stream":True})
st_c = ""
for l in raw.split('\n'):
    if l.startswith('data: ') and l.strip() != 'data: [DONE]':
        try:
            d = delta(json.loads(l[6:]))
            if d: st_c += d.get('content','') or ''
        except: pass
st_c = st_c.strip()
test("P7: content integrity (ns==st)", ns_c == st_c)

b = json.loads(chat({"model":"poolside/laguna-m.1:free","messages":[{"role":"user","content":"Get weather for Jakarta"}],
    "tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],
    "tool_choice":{"type":"function","function":{"name":"get_weather"}},"max_tokens":100}))
if 'error' not in b:
    m = b['choices'][0]['message']
    tc = m.get('tool_calls')
    test("P8: forced tool_call success", tc is not None)
    if tc:
        test("P8b: correct function name", tc[0]['function']['name'] == 'get_weather')
        test("P8c: has arguments", len(tc[0]['function'].get('arguments','')) > 0)
    test("P8d: reasoning key present", 'reasoning' in m)
    test("P8e: reasoning_content key present", 'reasoning_content' in m)
else:
    test("P8: forced tool_call", False, b['error']['message'][:60])

raw = chat({"model":"poolside/laguna-m.1:free","messages":[{"role":"user","content":"Get weather for Jakarta"}],
    "tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],
    "tool_choice":{"type":"function","function":{"name":"get_weather"}},"max_tokens":100,"stream":True})
lines = [l for l in raw.split('\n') if l.startswith('data: ') and l.strip() != 'data: [DONE]']
tc_chunks = 0
missing_dual = 0
for l in lines:
    try: c = json.loads(l[6:])
    except: continue
    d = delta(c)
    if d is None: continue
    if 'reasoning' not in d or 'reasoning_content' not in d: missing_dual += 1
    if 'tool_calls' in d: tc_chunks += 1
test("P9: streaming tool has tc_chunks", tc_chunks > 0)
test("P9b: streaming tool no missing_dual", missing_dual == 0)

# P10: Multi-turn conversation
t1 = json.loads(chat({"model":"openrouter/owl-alpha","messages":[{"role":"user","content":"Say HELLO"}],"max_tokens":5}))
c1 = (t1.get('choices',[{}])[0].get('message',{}).get('content','') or '').strip()
t2 = json.loads(chat({"model":"openrouter/owl-alpha","messages":[{"role":"user","content":"Say HELLO"},{"role":"assistant","content":c1},{"role":"user","content":"Now say WORLD"}],"max_tokens":5}))
c2 = (t2.get('choices',[{}])[0].get('message',{}).get('content','') or '').strip()
test("P10: multi-turn turn1 has content", bool(c1))
test("P10b: multi-turn turn2 has content", bool(c2))

# P11: Multi-turn tool cycle
t1 = json.loads(chat({"model":"poolside/laguna-m.1:free","messages":[{"role":"user","content":"Get weather for Jakarta"}],
    "tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],
    "tool_choice":{"type":"function","function":{"name":"get_weather"}},"max_tokens":100}))
if 'error' not in t1 and t1['choices'][0]['message'].get('tool_calls'):
    tc1 = t1['choices'][0]['message']['tool_calls'][0]
    t2 = json.loads(chat({"model":"poolside/laguna-m.1:free","messages":[
        {"role":"user","content":"Get weather for Jakarta"},
        {"role":"assistant","content":None,"tool_calls":[tc1]},
        {"role":"tool","tool_call_id":tc1['id'],"content":'{"temp":32,"condition":"sunny"}'}
    ],"max_tokens":100}))
    if 'error' not in t2:
        m2 = t2['choices'][0]['message']
        content = m2.get('content') or ''
        test("P11: multi-turn tool cycle", bool(content.strip()), f"content empty but dual keys verified")
        test("P11b: has reasoning key", 'reasoning' in m2)
        test("P11c: has reasoning_content key", 'reasoning_content' in m2)
    else:
        test("P11: multi-turn tool cycle", False, t2['error']['message'][:60])
else:
    err = t1.get('error',{}).get('message','no tool call in turn1')
    test("P11: multi-turn tool cycle", False, err[:60])

# P12: Long content streaming
raw = chat({"model":"openrouter/owl-alpha","messages":[{"role":"user","content":"Write 3 paragraphs about Go channels"}],"max_tokens":500,"stream":True})
lines = [l for l in raw.split('\n') if l.startswith('data: ') and l.strip() != 'data: [DONE]']
all_good = True
long_content = ""
for l in lines:
    try: c = json.loads(l[6:])
    except: all_good = False; continue
    d = delta(c)
    if d is not None:
        long_content += d.get('content','') or ''
        if 'reasoning' not in d or 'reasoning_content' not in d: all_good = False
test("P12: long stream all valid JSON", all_good)
test("P12b: long stream has content", len(long_content.strip()) > 100)

# P13: DeepSeek ns vs st integrity
ns = json.loads(chat({"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"Say hi in 2 words"}],"max_tokens":20}))
ns_c = (ns.get('choices',[{}])[0].get('message',{}).get('content','') or '').strip()
ns_r = (ns.get('choices',[{}])[0].get('message',{}).get('reasoning','') or '').strip()
raw = chat({"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"Say hi in 2 words"}],"max_tokens":20,"stream":True})
st_c = ""
st_r = ""
for l in raw.split('\n'):
    if l.startswith('data: ') and l.strip() != 'data: [DONE]':
        try:
            d = delta(json.loads(l[6:]))
            if d:
                st_c += d.get('content','') or ''
                st_r += d.get('reasoning','') or ''
        except: pass
test("P13: deepseek ns content == st content", ns_c.strip() == st_c.strip())
test("P13b: deepseek ns reasoning == st reasoning", ns_r.strip() == st_r.strip())

# P14: DeepSeek streaming tool call
raw = chat({"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"Calculate 2+2"}],
    "tools":[{"type":"function","function":{"name":"calc","description":"calculate","parameters":{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"},"op":{"type":"string"}},"required":["a","b","op"]}}}],
    "max_tokens":150,"stream":True})
lines = [l for l in raw.split('\n') if l.startswith('data: ') and l.strip() != 'data: [DONE]']
tc_chunks = 0
missing_dual = 0
for l in lines:
    try: c = json.loads(l[6:])
    except: continue
    d = delta(c)
    if d is None: continue
    if 'reasoning' not in d or 'reasoning_content' not in d: missing_dual += 1
    if 'tool_calls' in d: tc_chunks += 1
test("P14: deepseek stream tool tc_chunks", tc_chunks > 0)
test("P14b: deepseek stream tool no missing_dual", missing_dual == 0)


# ═══════════════════════════════════════════════
# NEGATIVE CASES
# ═══════════════════════════════════════════════

print()
print("=" * 50)
print("NEGATIVE / EDGE CASES")
print("=" * 50)

s, b = get("/v1/nonexistent")
test("N1: non-existent endpoint 404", s == 404 or s in [400,405])

conn = http.client.HTTPSConnection(BASE, timeout=10)
conn.request("POST", "/ready", headers={"User-Agent": UA})
resp = conn.getresponse(); resp.read(); conn.close()
test("N2: POST /ready not 200", resp.status != 200)

b = json.loads(chat({"model":"","messages":[{"role":"user","content":"hi"}],"max_tokens":5}))
test("N3: empty model ID returns error", 'error' in b)

b = json.loads(chat({"model":"openrouter/owl-alpha","messages":[],"max_tokens":5}))
test("N4: empty messages returns error", 'error' in b or b.get('choices',[{}])[0].get('finish_reason','') != 'stop')

conn = http.client.HTTPSConnection(BASE, timeout=10)
conn.request("POST", "/v1/chat/completions", "", {"Content-Type":"application/json","User-Agent":UA})
resp = conn.getresponse(); b_raw = resp.read().decode(); conn.close()
test("N5: empty body returns error", resp.status == 400 or 'error' in b_raw)

conn = http.client.HTTPSConnection(BASE, timeout=10)
conn.request("POST", "/v1/chat/completions", "not-json-{", {"Content-Type":"application/json","User-Agent":UA})
resp = conn.getresponse(); b_raw = resp.read().decode(); conn.close()
test("N6: invalid JSON returns error", 'error' in b_raw)

b = json.loads(chat({"messages":[{"role":"user","content":"hi"}],"max_tokens":5}))
test("N7: missing model returns error", 'error' in b)

b = json.loads(chat({"model":"this-model-definitely-does-not-exist-12345","messages":[{"role":"user","content":"hi"}],"max_tokens":5}))
test("N8: non-existent model returns error", 'error' in b)

try:
    huge = {"model":"openrouter/owl-alpha","messages":[{"role":"user","content":"x" * (12 * 1024 * 1024)}]}
    b = json.loads(chat(huge))
    test("N9: oversized body rejected", 'error' in b)
except Exception as e:
    test("N9: oversized body rejected (connection error)", True)

b = json.loads(chat({"model":"a" * 1000,"messages":[{"role":"user","content":"hi"}],"max_tokens":5}))
test("N10: very long model ID returns error", 'error' in b)

s, b = get("/v1/chat/completions")
test("N11: GET /v1/chat/completions", s != 200 or 'error' in b)

b = json.loads(chat({"model":"kilo/nonexistent-model-name","messages":[{"role":"user","content":"hi"}],"max_tokens":5}))
test("N12: nonexistent kilo returns error", 'error' in b)

b = json.loads(chat({"model":"openrouter/owl-alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":0}))
test("N13: zero max_tokens no crash", 'error' not in b)

b = json.loads(chat({"model":"openrouter/owl-alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":-1}))
test("N14: negative max_tokens no crash", 'error' not in b)

b = json.loads(chat({"model":"openrouter/owl-alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":None}))
test("N15: null max_tokens no crash", 'error' not in b)


# ═══════════════════════════════════════════════
# SUMMARY
# ═══════════════════════════════════════════════

print()
print("=" * 50)
total = passed + failed + len(skipped)
skip_str = f" ({len(skipped)} skipped)" if skipped else ""
print(f"SUMMARY: {passed} passed, {failed} failed out of {total} tests{skip_str}")
print("=" * 50)
sys.exit(0 if failed == 0 else 1)
