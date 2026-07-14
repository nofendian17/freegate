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
		// Truncated string value: must close the string before the object,
		// else the client rejects it with "input JSON failed to parse".
		"truncated-string": `{"command":"rm -rf /tmp/foo`,
		// Empty value: unrecoverable, but output must still be valid JSON.
		"empty-value": `{"a":}`,
	}
	for label, in := range cases {
		t.Run(label, func(t *testing.T) {
			assertValid(t, in, repairToolArgs(in))
		})
	}
}

// TestRepairToolArgs_NeverReturnsInvalidJSON guards the contract that
// repairToolArgs always yields parseable JSON: a malformed buffer that cannot
// be salvaged degrades to "{}" rather than being forwarded verbatim (which
// Claude Code surfaces as "Bash(input JSON failed to parse — N bytes)").
func TestRepairToolArgs_NeverReturnsInvalidJSON(t *testing.T) {
	if out := repairToolArgs(`{"a":}`); out != "{}" {
		t.Errorf("expected give-up to return %q, got %q", "{}", out)
	}
}
