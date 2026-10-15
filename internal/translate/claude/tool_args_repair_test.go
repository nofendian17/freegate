package claude

import (
	"encoding/json"
	"strings"
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
		"historical-dup":        `{"file_path":"/Users/xxx/.mcp.json","content":"{\n  \"mcpServers\": {}\n}\n"}{"file_path":"/Users/beninofendianwar/.mcp.json","content":"{\n  \"mcpServers\": {}\n}\n"}`,
		"trailing-comma":        `{"a":1,}`,
		"trailing-comma-nested": `{"a":1,"b":[2,3,]}`,
		"unclosed-brace":        `{"a":1`,
		"unclosed-nested":       `{"a":{"b":1`,
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

// TestRepairToolArgs_Int64PrecisionPreserved guards the fast path against
// json.Unmarshal's float64 round-trip: large integer tool arguments
// (snowflake IDs, nanosecond timestamps) must keep their exact digits
// instead of being reformatted as 1.2345678901234568e+18.
func TestRepairToolArgs_Int64PrecisionPreserved(t *testing.T) {
	const digits = "1234567890123456789"
	for _, in := range []string{
		`{"id":1234567890123456789}`,
		"{\"id\":1234567890123456789}", // double-encoded object
	} {
		if out := repairToolArgs(in); !strings.Contains(out, digits) {
			t.Errorf("repairToolArgs(%q) = %q, int64 digits lost", in, out)
		}
	}
}

// TestSplitToolArgs_Int64PrecisionPreserved: split parts run through the
// same repair fast path, so digits must survive the split too.
func TestSplitToolArgs_Int64PrecisionPreserved(t *testing.T) {
	parts := splitToolArgs(`{"a":1}{"b":9223372036854775807}`)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %q", len(parts), parts)
	}
	if !strings.Contains(parts[1], "9223372036854775807") {
		t.Errorf("second part lost int64 digits: %q", parts[1])
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

// TestRepairToolArgs_AlwaysObject guards the contract that tool_use input
// must be a JSON object. Models such as tencent/hy3-free emit the arguments
// as a bare JSON string, an array, or a string that itself encodes an object
// ("{\"cmd\":\"ls\"}"); emitting those verbatim makes the client reject the
// tool_use with "input JSON failed to parse — N bytes". Any non-object result
// must be normalized to "{}" (a bare string that re-parses to an object is
// unwrapped first).
func TestRepairToolArgs_AlwaysObject(t *testing.T) {
	cases := map[string]string{
		"bare-string":        `"hello"`,
		"bare-array":         `[1,2,3]`,
		"bare-number":        `42`,
		"bare-bool":          `true`,
		"bare-null":          `null`,
		"stringified-object": `"{\"cmd\":\"ls -la\"}"`,
		"object":             `{"cmd":"ls -la"}`,
		"unescaped-quotes":   `{"cmd":"echo "hi""}`,
	}
	for label, in := range cases {
		t.Run(label, func(t *testing.T) {
			out := repairToolArgs(in)
			var v any
			if err := json.Unmarshal([]byte(out), &v); err != nil {
				t.Fatalf("repairToolArgs(%q) -> %q : INVALID JSON: %v", in, out, err)
			}
			if _, ok := v.(map[string]any); !ok {
				t.Fatalf("repairToolArgs(%q) -> %q : not a JSON object (got %T)", in, out, v)
			}
		})
	}
}

// TestRepairToolArgs_StringifiedObjectUnwrapped verifies a double-encoded
// object string is unwrapped to the object, preserving the arguments.
func TestRepairToolArgs_StringifiedObjectUnwrapped(t *testing.T) {
	out := repairToolArgs(`"{\"cmd\":\"ls -la\"}"`)
	if out != `{"cmd":"ls -la"}` {
		t.Errorf("expected unwrapped object, got %q", out)
	}
}
