package claude

import (
	"encoding/json"
	"testing"
)

func assertValid(t *testing.T, in, out string) {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Errorf("repairToolArgs(%q) -> %q : INVALID JSON: %v", in, out, err)
	}
}

func TestRepairToolArgs_AllValid(t *testing.T) {
	cases := map[string]string{
		"historical-dup":        `{"file_path":"/Users/beninofendianwar/.mcp.json","content":"{\n  \"mcpServers\": {}\n}\n"}{"file_path":"/Users/beninofendianwar/.mcp.json","content":"{\n  \"mcpServers\": {}\n}\n"}`,
		"trailing-comma":        `{"a":1,}`,
		"trailing-comma-nested": `{"a":1,"b":[2,3,]}`,
		"unclosed-brace":       `{"a":1`,
		"unclosed-nested":      `{"a":{"b":1`,
	}
	for label, in := range cases {
		t.Run(label, func(t *testing.T) {
			assertValid(t, in, repairToolArgs(in))
		})
	}
}
