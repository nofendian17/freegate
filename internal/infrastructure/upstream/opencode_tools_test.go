package upstream

import (
	"encoding/json"
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

func expectGateNames(t *testing.T, tools []any) {
	t.Helper()
	want := []string{
		"bash", "edit", "glob", "grep", "read", "skill", "task",
		"todowrite", "webfetch", "websearch", "write",
		"question", "apply_patch", "execute", "lsp", "plan_exit",
	}
	if len(tools) != len(want) {
		t.Fatalf("tools=%v, want %d stubs", toolNames(tools), len(want))
	}
	got := toolNames(tools)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tools=%v, want %v", got, want)
		}
	}
}

func TestEnsureUpstreamTools_Chat(t *testing.T) {
	out := ensureUpstreamTools([]byte(`{"model":"mimo-v2.5-free","messages":[]}`), "/chat/completions")
	tools := toolsOf(t, out)
	expectGateNames(t, tools)
	fn, ok := tools[0].(map[string]any)["function"].(map[string]any)
	if !ok {
		t.Fatalf("not openai function shape: %v", tools[0])
	}
	if _, ok := fn["parameters"].(map[string]any); !ok {
		t.Fatalf("missing parameters: %v", fn)
	}
}

func TestEnsureUpstreamTools_Messages(t *testing.T) {
	out := ensureUpstreamTools([]byte(`{"model":"union-alpha","messages":[]}`), "/messages")
	tools := toolsOf(t, out)
	expectGateNames(t, tools)
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["input_schema"] == nil {
		t.Fatalf("not claude shape: %v", tools[0])
	}
}

func TestEnsureUpstreamTools_Responses(t *testing.T) {
	out := ensureUpstreamTools([]byte(`{"model":"muse-spark","input":[]}`), "/responses")
	tools := toolsOf(t, out)
	expectGateNames(t, tools)
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["type"] != "function" || tool["strict"] != false {
		t.Fatalf("not responses shape: %v", tools[0])
	}
}

func TestEnsureUpstreamTools_Passthrough(t *testing.T) {
	// Non-empty tools untouched.
	in := `{"model":"x","messages":[],"tools":[{"type":"function","function":{"name":"real"}}]}`
	if out := ensureUpstreamTools([]byte(in), "/chat/completions"); string(out) != in {
		t.Fatalf("rewrote body with tools: %s", out)
	}
	// Unparseable untouched.
	if out := ensureUpstreamTools([]byte(`{oops`), "/chat/completions"); string(out) != `{oops` {
		t.Fatalf("rewrote invalid body: %s", out)
	}
	// Present-but-malformed tools pass through for a clear upstream 4xx.
	in = `{"model":"x","messages":[],"tools":"nope"}`
	if out := ensureUpstreamTools([]byte(in), "/chat/completions"); string(out) != in {
		t.Fatalf("rewrote malformed tools: %s", out)
	}
	// Empty array gets the stubs (gate rejects []).
	out := ensureUpstreamTools([]byte(`{"model":"x","messages":[],"tools":[]}`), "/chat/completions")
	expectGateNames(t, toolsOf(t, out))
	// Other fields preserved.
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["model"] != "x" || !strings.Contains(string(out), `"messages":[]`) {
		t.Fatalf("fields lost: %s", out)
	}
}
