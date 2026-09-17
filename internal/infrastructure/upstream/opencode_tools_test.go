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

func TestEnsureUpstreamTools_Chat(t *testing.T) {
	out := ensureUpstreamTools([]byte(`{"model":"mimo-v2.5-free","messages":[]}`), "/chat/completions")
	tools := toolsOf(t, out)
	if len(tools) != 1 {
		t.Fatalf("tools=%v", tools)
	}
	fn, ok := tools[0].(map[string]any)["function"].(map[string]any)
	if !ok || fn["name"] != "_noop" {
		t.Fatalf("not openai function shape: %v", tools[0])
	}
}

func TestEnsureUpstreamTools_Messages(t *testing.T) {
	out := ensureUpstreamTools([]byte(`{"model":"union-alpha","messages":[]}`), "/messages")
	tools := toolsOf(t, out)
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["name"] != "_noop" || tool["input_schema"] == nil {
		t.Fatalf("not claude shape: %v", tools[0])
	}
}

func TestEnsureUpstreamTools_Responses(t *testing.T) {
	out := ensureUpstreamTools([]byte(`{"model":"muse-spark","input":[]}`), "/responses")
	tools := toolsOf(t, out)
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["type"] != "function" || tool["name"] != "_noop" || tool["strict"] != false {
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
	// Empty array gets the noop (gate rejects []).
	out := ensureUpstreamTools([]byte(`{"model":"x","messages":[],"tools":[]}`), "/chat/completions")
	if len(toolsOf(t, out)) != 1 {
		t.Fatalf("empty tools not filled: %s", out)
	}
	// Other fields preserved.
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["model"] != "x" || !strings.Contains(string(out), `"messages":[]`) {
		t.Fatalf("fields lost: %s", out)
	}
}
