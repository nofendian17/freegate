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
	// Unparseable untouched.
	if out := ensureUpstreamTools([]byte(`{oops`), "/chat/completions"); string(out) != `{oops` {
		t.Fatalf("rewrote invalid body: %s", out)
	}
	// Present-but-malformed tools pass through for a clear upstream 4xx.
	in := `{"model":"x","messages":[],"tools":"nope"}`
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

func TestEnsureUpstreamTools_MergesForeignTools(t *testing.T) {
	// Foreign-only tools (e.g. Hermes browser_*/clarify) drew 403
	// FreeTierError live 2026-09-19; the gate needs its canonical names
	// present. Caller tools are preserved, missing gate names appended.
	in := `{"model":"muse-spark","input":[],"tools":[{"type":"function","name":"browser_navigate","description":"x","parameters":{"type":"object"}},{"type":"function","name":"clarify","description":"y","parameters":{"type":"object"}}]}`
	out := ensureUpstreamTools([]byte(in), "/responses")
	tools := toolsOf(t, out)
	got := toolNames(tools)
	if len(tools) != 2+len(gateToolNames) {
		t.Fatalf("tools=%v, want 2 foreign + %d gate stubs", got, len(gateToolNames))
	}
	if got[0] != "browser_navigate" || got[1] != "clarify" {
		t.Fatalf("caller tools not preserved in order: %v", got)
	}
	seen := make(map[string]bool)
	for _, n := range got {
		seen[n] = true
	}
	for _, want := range gateToolNames {
		if !seen[want] {
			t.Fatalf("missing gate tool %q in %v", want, got)
		}
	}
	// Appended stubs use the endpoint native shape.
	last, _ := tools[len(tools)-1].(map[string]any)
	if last["type"] != "function" || last["strict"] != false {
		t.Fatalf("merged stub not responses shape: %v", last)
	}
}

func TestEnsureUpstreamTools_NoDupWhenComplete(t *testing.T) {
	// A body already carrying the full gate set is byte-identical.
	var parts []string
	for _, n := range gateToolNames {
		parts = append(parts, `{"type":"function","name":"`+n+`"}`)
	}
	in := `{"model":"muse-spark","input":[],"tools":[` + strings.Join(parts, ",") + `]}`
	if out := ensureUpstreamTools([]byte(in), "/responses"); string(out) != in {
		t.Fatalf("rewrote complete body: %s", out)
	}
}
