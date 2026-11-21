package upstream

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func toolsOf(t *testing.T, body []byte) []any {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	tools, ok := raw["tools"].([]any)
	if !ok {
		t.Fatalf("no tools array: %s", body)
	}
	return tools
}

func toolNames(tools []any) []string {
	var out []string
	for _, item := range tools {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		if fn, ok := m["function"].(map[string]any); ok {
			m = fn
		}
		if name, ok := m["name"].(string); ok {
			out = append(out, name)
		}
	}
	return out
}

func expectFingerprintNames(t *testing.T, tools []any) {
	t.Helper()
	got := toolNames(tools)
	if len(got) != len(fingerprintToolNames) {
		t.Fatalf("tools=%v, want %v", got, fingerprintToolNames)
	}
	for i := range fingerprintToolNames {
		if got[i] != fingerprintToolNames[i] {
			t.Fatalf("tools=%v, want %v", got, fingerprintToolNames)
		}
	}
}

func TestEnsureUpstreamTools_Chat(t *testing.T) {
	out, aliases := ensureUpstreamTools([]byte(`{"model":"mimo-v2.5-free","messages":[]}`), "/chat/completions")
	if len(aliases) != 0 {
		t.Fatalf("aliases=%v, want none", aliases)
	}
	tools := toolsOf(t, out)
	expectFingerprintNames(t, tools)
	fn, ok := tools[0].(map[string]any)["function"].(map[string]any)
	if !ok {
		t.Fatalf("not openai function shape: %v", tools[0])
	}
	if _, ok := fn["parameters"].(map[string]any); !ok {
		t.Fatalf("missing parameters: %v", fn)
	}
	if desc, _ := fn["description"].(string); !strings.HasPrefix(desc, "Read the contents") {
		t.Fatalf("read description not verbatim: %q", desc)
	}
}

func TestEnsureUpstreamTools_Messages(t *testing.T) {
	out, _ := ensureUpstreamTools([]byte(`{"model":"union-alpha","messages":[]}`), "/messages")
	tools := toolsOf(t, out)
	expectFingerprintNames(t, tools)
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["input_schema"] == nil {
		t.Fatalf("not claude shape: %v", tools[0])
	}
}

func TestEnsureUpstreamTools_Responses(t *testing.T) {
	out, _ := ensureUpstreamTools([]byte(`{"model":"muse-spark","input":[]}`), "/responses")
	tools := toolsOf(t, out)
	expectFingerprintNames(t, tools)
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["type"] != "function" || tool["strict"] != false {
		t.Fatalf("not responses shape: %v", tools[0])
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["tool_choice"] != "auto" {
		t.Fatalf("responses tool_choice=%v, want auto", raw["tool_choice"])
	}
}

func TestEnsureUpstreamTools_Passthrough(t *testing.T) {
	// Unparseable untouched.
	if out, _ := ensureUpstreamTools([]byte(`{oops`), "/chat/completions"); string(out) != `{oops` {
		t.Fatalf("rewrote invalid body: %s", out)
	}
	// Present-but-malformed tools pass through for a clear upstream 4xx.
	in := `{"model":"x","messages":[],"tools":"nope"}`
	if out, _ := ensureUpstreamTools([]byte(in), "/chat/completions"); string(out) != in {
		t.Fatalf("rewrote malformed tools: %s", out)
	}
	// Empty array gets the exact fingerprint (gate rejects []).
	out, _ := ensureUpstreamTools([]byte(`{"model":"x","messages":[],"tools":[]}`), "/chat/completions")
	expectFingerprintNames(t, toolsOf(t, out))
	// Other fields preserved (messages gains the placeholder system note).
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["model"] != "x" {
		t.Fatalf("fields lost: %s", out)
	}
}

func TestEnsureUpstreamTools_MergesForeignTools(t *testing.T) {
	in := `{"model":"muse-spark","input":[],"tools":[{"type":"function","name":"browser_navigate","description":"x","parameters":{"type":"object"}},{"type":"function","name":"clarify","description":"y","parameters":{"type":"object"}}]}`
	out, aliases := ensureUpstreamTools([]byte(in), "/responses")
	if len(aliases) != 0 {
		t.Fatalf("aliases=%v, want none", aliases)
	}
	tools := toolsOf(t, out)
	got := toolNames(tools)
	if len(tools) != 2+len(fingerprintToolNames) {
		t.Fatalf("tools=%v, want 2 foreign + %d fingerprint", got, len(fingerprintToolNames))
	}
	if got[0] != "browser_navigate" || got[1] != "clarify" {
		t.Fatalf("caller tools not preserved in order: %v", got)
	}
	// Appended stubs use the endpoint native shape.
	last, _ := tools[len(tools)-1].(map[string]any)
	if last["type"] != "function" || last["strict"] != false {
		t.Fatalf("merged stub not responses shape: %v", last)
	}
}

func TestEnsureUpstreamTools_CaseVariantReadAliased(t *testing.T) {
	// Claude Code's Read is a case variant of the fingerprint name, so it is
	// concealed; Bash does not collide and passes through. Upstream rejects a
	// `Read` tool carrying `file_path` even next to the exact fingerprint.
	in := `{"model":"m","messages":[],"tools":[{"type":"function","function":{"name":"Read","description":"Reads a file","parameters":{"type":"object","properties":{"file_path":{"type":"string"}}}}},{"type":"function","function":{"name":"Bash","description":"y"}}]}`
	out, aliases := ensureUpstreamTools([]byte(in), "/chat/completions")
	if aliases["read_client"] != "Read" {
		t.Fatalf("aliases=%v, want read_client->Read", aliases)
	}
	got := toolNames(toolsOf(t, out))
	want := []string{"read_client", "Bash", "read", "shell"}
	if len(got) != len(want) {
		t.Fatalf("tools=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tools=%v, want %v", got, want)
		}
	}
	// Aliasing must change the name only: description and parameters stay
	// byte-identical so the model sees exactly the caller's tool definition.
	origFn := decodeBody(t, []byte(in))["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	sentFn := toolsOf(t, out)[0].(map[string]any)["function"].(map[string]any)
	if sentFn["description"] != origFn["description"] ||
		!reflect.DeepEqual(canonicalJSON(sentFn["parameters"]), canonicalJSON(origFn["parameters"])) {
		t.Fatalf("alias changed tool definition: orig=%v sent=%v", origFn, sentFn)
	}

	// The response restores the caller's exact spelling, not "read".
	restored := restoreUpstreamBody([]byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"read_client"}}]}}]}`), aliases)
	if !strings.Contains(string(restored), `"Read"`) || strings.Contains(string(restored), "read_client") {
		t.Fatalf("response not restored to Read: %s", restored)
	}
}

func TestEnsureUpstreamTools_RewritesMessagesHistory(t *testing.T) {
	in := `{"model":"m","system":"s","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/a"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"hi"}]}],"tools":[{"name":"Read","description":"Reads a file","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}]}`
	out, aliases := ensureUpstreamTools([]byte(in), "/messages")
	if aliases["read_client"] != "Read" {
		t.Fatalf("aliases=%v, want read_client->Read", aliases)
	}
	if strings.Contains(string(out), `"name":"Read"`) {
		t.Fatalf("history tool_use name not aliased: %s", out)
	}
	if !strings.Contains(string(out), `"name":"read_client"`) {
		t.Fatalf("history tool_use name not aliased: %s", out)
	}
}

func TestEnsureUpstreamTools_AliasesConflictingCallerTool(t *testing.T) {
	in := `{"model":"m","messages":[],"tools":[{"type":"function","function":{"name":"read","description":"existing"}}]}`
	out, aliases := ensureUpstreamTools([]byte(in), "/chat/completions")
	if aliases["read_client"] != "read" {
		t.Fatalf("aliases=%v, want read_client->read", aliases)
	}
	got := toolNames(toolsOf(t, out))
	want := []string{"read_client", "read", "shell"}
	if len(got) != len(want) {
		t.Fatalf("tools=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tools=%v, want %v", got, want)
		}
	}
	restored := restoreUpstreamBody([]byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"read_client"}}]}}]}`), aliases)
	if !strings.Contains(string(restored), `"read"`) || strings.Contains(string(restored), "read_client") {
		t.Fatalf("response not restored: %s", restored)
	}
}

func TestEnsureUpstreamTools_KeepsExactDefinition(t *testing.T) {
	first, _ := ensureUpstreamTools([]byte(`{"model":"m","input":[]}`), "/responses")
	var raw map[string]any
	if err := json.Unmarshal(first, &raw); err != nil {
		t.Fatal(err)
	}
	official, _ := json.Marshal(map[string]any{"model": "m", "input": []any{}, "tools": raw["tools"]})
	second, aliases := ensureUpstreamTools(official, "/responses")
	if len(aliases) != 0 {
		t.Fatalf("aliases=%v, want none for exact definitions", aliases)
	}
	if got := toolNames(toolsOf(t, second)); len(got) != 2 {
		t.Fatalf("tools=%v, want exactly read+shell", got)
	}
}

func TestEnsureUpstreamTools_StripsNoneToolChoice(t *testing.T) {
	out, _ := ensureUpstreamTools([]byte(`{"model":"m","messages":[],"tool_choice":"none"}`), "/chat/completions")
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["tool_choice"]; ok {
		t.Fatalf("tool_choice none not stripped: %s", out)
	}
}

func TestEnsureUpstreamTools_PlaceholderNote(t *testing.T) {
	out, _ := ensureUpstreamTools([]byte(`{"model":"muse-spark","input":[],"instructions":"Be brief."}`), "/responses")
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	ins, _ := raw["instructions"].(string)
	if !strings.HasPrefix(ins, "Be brief.\n\nDo not call the `read` or `shell` tools;") {
		t.Fatalf("instructions note missing: %q", ins)
	}
	chatOut, _ := ensureUpstreamTools([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), "/chat/completions")
	var chatRaw map[string]any
	if err := json.Unmarshal(chatOut, &chatRaw); err != nil {
		t.Fatal(err)
	}
	msgs, _ := chatRaw["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected system note prepended, got %s", chatOut)
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || !strings.Contains(first["content"].(string), "Do not call") {
		t.Fatalf("system note missing: %s", chatOut)
	}
}

func TestFingerprintToolKey_CaseInsensitive(t *testing.T) {
	if fingerprintToolKey("read") != "read" || fingerprintToolKey(" shell ") != "shell" {
		t.Fatal("exact match broken")
	}
	if fingerprintToolKey("Read") != "read" || fingerprintToolKey("READ") != "read" || fingerprintToolKey("SHELL") != "shell" {
		t.Fatal("case-insensitive match broken")
	}
	for _, n := range []string{"Bash", "bash", "terminal", ""} {
		if fingerprintToolKey(n) != "" {
			t.Fatalf("expected empty for %q", n)
		}
	}
}

func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return raw
}

func firstSystemContent(t *testing.T, raw map[string]any) string {
	t.Helper()
	msgs, _ := raw["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" {
		return ""
	}
	content, _ := first["content"].(string)
	return content
}

func TestEnsureUpstreamTools_InjectsEnvChat(t *testing.T) {
	out, _ := ensureUpstreamTools([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), "/chat/completions")
	raw := decodeBody(t, out)
	msgs, _ := raw["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages=%d, want 2: %s", len(msgs), out)
	}
	content := firstSystemContent(t, raw)
	if !strings.Contains(content, "Do not call the `read` or `shell` tools") {
		t.Fatalf("placeholder note missing: %q", content)
	}
	if !strings.Contains(content, "<env>") || !strings.Contains(content, "</env>") {
		t.Fatalf("env block missing: %q", content)
	}
	if !strings.Contains(content, "Working directory: ") || !strings.Contains(content, "Platform: ") {
		t.Fatalf("env block missing fields: %q", content)
	}
	if !strings.Contains(content, "Workspace root folder: ") || !strings.Contains(content, "Is directory a git repo: ") {
		t.Fatalf("env block missing dev fields: %q", content)
	}

	// A second pass must not inject a duplicate block.
	out2, _ := ensureUpstreamTools(out, "/chat/completions")
	if got := strings.Count(firstSystemContent(t, decodeBody(t, out2)), "<env>"); got != 1 {
		t.Fatalf("env present %d times after second pass: %s", got, out2)
	}
}

func TestEnsureUpstreamTools_InjectsEnvPreservesSystem(t *testing.T) {
	out, _ := ensureUpstreamTools([]byte(`{"model":"m","messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"hi"}]}`), "/chat/completions")
	content := firstSystemContent(t, decodeBody(t, out))
	if !strings.HasPrefix(content, "Be brief.\n\nDo not call") {
		t.Fatalf("existing system prompt not preserved: %q", content)
	}
	if !strings.HasSuffix(content, "</env>") {
		t.Fatalf("env block not appended: %q", content)
	}
}

func TestEnsureUpstreamTools_InjectsEnvMessages(t *testing.T) {
	out, _ := ensureUpstreamTools([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), "/messages")
	raw := decodeBody(t, out)
	sys, _ := raw["system"].(string)
	if !strings.Contains(sys, "Do not call") || !strings.Contains(sys, "<env>") {
		t.Fatalf("messages system prompt missing note/env: %q", sys)
	}
	msgs, _ := raw["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages=%d, want the caller's single message: %s", len(msgs), out)
	}
}

func TestEnsureUpstreamTools_InjectsEnvMessagesSystemArray(t *testing.T) {
	out, _ := ensureUpstreamTools([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"system":[{"type":"text","text":"Be brief."}]}`), "/messages")
	raw := decodeBody(t, out)
	blocks, _ := raw["system"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("system blocks=%d, want 3 (prompt, note, env): %s", len(blocks), out)
	}
	first, _ := blocks[0].(map[string]any)
	if first["text"] != "Be brief." {
		t.Fatalf("existing block not preserved: %v", blocks[0])
	}
	last, _ := blocks[len(blocks)-1].(map[string]any)
	if text, _ := last["text"].(string); !strings.Contains(text, "<env>") {
		t.Fatalf("env block missing: %v", last)
	}
}

func TestEnsureUpstreamTools_InjectsEnvResponses(t *testing.T) {
	out, _ := ensureUpstreamTools([]byte(`{"model":"m","input":[],"instructions":"Be brief."}`), "/responses")
	raw := decodeBody(t, out)
	ins, _ := raw["instructions"].(string)
	if !strings.HasPrefix(ins, "Be brief.\n\nDo not call") || !strings.Contains(ins, "<env>") {
		t.Fatalf("responses instructions missing note/env: %q", ins)
	}

	out2, _ := ensureUpstreamTools([]byte(`{"model":"m","input":[]}`), "/responses")
	raw2 := decodeBody(t, out2)
	ins2, _ := raw2["instructions"].(string)
	if !strings.Contains(ins2, "<env>") {
		t.Fatalf("responses instructions not created: %q", ins2)
	}
}

func TestEnsureUpstreamTools_DoesNotDuplicateEnv(t *testing.T) {
	in := `{"model":"m","input":[],"instructions":"<env>\n  Working directory: /tmp\n  Platform: linux\n</env>"}`
	out, _ := ensureUpstreamTools([]byte(in), "/responses")
	ins, _ := decodeBody(t, out)["instructions"].(string)
	if got := strings.Count(ins, "<env>"); got != 1 {
		t.Fatalf("env present %d times, want 1: %s", got, out)
	}

	chatIn := `{"model":"m","messages":[{"role":"system","content":"<env>\n  Working directory: /tmp\n  Platform: linux\n</env>"},{"role":"user","content":"hi"}]}`
	chatOut, _ := ensureUpstreamTools([]byte(chatIn), "/chat/completions")
	if got := strings.Count(firstSystemContent(t, decodeBody(t, chatOut)), "<env>"); got != 1 {
		t.Fatalf("chat env present %d times, want 1: %s", got, chatOut)
	}
}

func TestEnsureUpstreamTools_InjectsEnvChatContentParts(t *testing.T) {
	out, _ := ensureUpstreamTools([]byte(`{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"Be brief."}]},{"role":"user","content":"hi"}]}`), "/chat/completions")
	raw := decodeBody(t, out)
	msgs, _ := raw["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages=%d, want 2: %s", len(msgs), out)
	}
	first, _ := msgs[0].(map[string]any)
	parts, _ := first["content"].([]any)
	if len(parts) != 3 {
		t.Fatalf("content parts=%d, want 3 (prompt, note, env): %s", len(parts), out)
	}
	last, _ := parts[len(parts)-1].(map[string]any)
	if text, _ := last["text"].(string); !strings.Contains(text, "<env>") {
		t.Fatalf("env block missing: %v", last)
	}
}
