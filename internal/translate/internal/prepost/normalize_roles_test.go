package prepost

import (
	"encoding/json"
	"testing"
)

func TestNormalizeRoles(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		check func(t *testing.T, out string)
	}{
		{
			name: "single developer message converted to system",
			in:   `{"messages":[{"role":"developer","content":"be helpful"}]}`,
			check: func(t *testing.T, out string) {
				var raw map[string]any
				if err := json.Unmarshal([]byte(out), &raw); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
				msgs := raw["messages"].([]any)
				m := msgs[0].(map[string]any)
				if m["role"] != "system" {
					t.Errorf("expected role=system, got %v", m["role"])
				}
			},
		},
		{
			name: "multiple developer messages all converted",
			in:   `{"messages":[{"role":"developer","content":"a"},{"role":"user","content":"hi"},{"role":"developer","content":"b"}]}`,
			check: func(t *testing.T, out string) {
				var raw map[string]any
				if err := json.Unmarshal([]byte(out), &raw); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
				msgs := raw["messages"].([]any)
				roles := []string{
					msgs[0].(map[string]any)["role"].(string),
					msgs[1].(map[string]any)["role"].(string),
					msgs[2].(map[string]any)["role"].(string),
				}
				if roles[0] != "system" || roles[1] != "user" || roles[2] != "system" {
					t.Errorf("expected [system user system], got %v", roles)
				}
			},
		},
		{
			name: "mixed roles, only developer changed",
			in:   `{"messages":[{"role":"system","content":"sys"},{"role":"developer","content":"dev"},{"role":"user","content":"hi"},{"role":"assistant","content":"hey"}]}`,
			check: func(t *testing.T, out string) {
				var raw map[string]any
				if err := json.Unmarshal([]byte(out), &raw); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
				msgs := raw["messages"].([]any)
				roles := []string{
					msgs[0].(map[string]any)["role"].(string),
					msgs[1].(map[string]any)["role"].(string),
					msgs[2].(map[string]any)["role"].(string),
					msgs[3].(map[string]any)["role"].(string),
				}
				want := []string{"system", "system", "user", "assistant"}
				for i, r := range roles {
					if r != want[i] {
						t.Errorf("msg[%d]: expected role=%q, got %q", i, want[i], r)
					}
				}
			},
		},
		{
			name: "developer in content, not role, body unchanged",
			in:   `{"messages":[{"role":"user","content":"you are a developer"}]}`,
			check: func(t *testing.T, out string) {
				// ""developer"" (with quotes) should NOT appear in content,
				// so the cheap byte scan won't trigger the parse path.
				// The body should be byte-identical.
				if out != `{"messages":[{"role":"user","content":"you are a developer"}]}` {
					t.Errorf("body was unexpectedly modified: %s", out)
				}
			},
		},
		{
			name: "no developer role, body unchanged",
			in:   `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`,
			check: func(t *testing.T, out string) {
				if out != `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}` {
					t.Errorf("body was modified: %s", out)
				}
			},
		},
		{
			name: "empty messages, body unchanged",
			in:   `{"messages":[]}`,
			check: func(t *testing.T, out string) {
				if out != `{"messages":[]}` {
					t.Errorf("body was modified: %s", out)
				}
			},
		},
		{
			name: "preserves other fields",
			in:   `{"model":"deepseek-v4-flash","messages":[{"role":"developer","content":"be helpful"}],"temperature":0.7}`,
			check: func(t *testing.T, out string) {
				var raw map[string]any
				if err := json.Unmarshal([]byte(out), &raw); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
				if raw["model"] != "deepseek-v4-flash" {
					t.Errorf("model lost, got %v", raw["model"])
				}
				if temp, ok := raw["temperature"].(float64); !ok || temp != 0.7 {
					t.Errorf("temperature lost, got %v", raw["temperature"])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outBytes, err := NormalizeRoles([]byte(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc.check(t, string(outBytes))
		})
	}
}

func TestNormalizeRoles_Empty(t *testing.T) {
	out, err := NormalizeRoles(nil)
	if err != nil || out != nil {
		t.Errorf("nil body should passthrough, got out=%q err=%v", out, err)
	}

	out, err = NormalizeRoles([]byte{})
	if err != nil || len(out) != 0 {
		t.Errorf("empty body should passthrough, got out=%q err=%v", out, err)
	}
}

func TestNormalizeRoles_NoMessagesKey(t *testing.T) {
	body := `{"model":"gpt-4","stream":true}`
	out, err := NormalizeRoles([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should be unchanged because byte scan won't find "developer"
	if string(out) != body {
		t.Errorf("body modified: got %s", string(out))
	}
}

func TestNormalizeRoles_ContentContainsDeveloperString(t *testing.T) {
	// The byte scan for "developer" triggers even when it appears in
	// content. Verify we only change role fields, not content text.
	body := []byte(`{"messages":[{"role":"user","content":"the \"developer\" role is deprecated"}]}`)
	out, err := NormalizeRoles(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	msgs := raw["messages"].([]any)
	m := msgs[0].(map[string]any)
	if m["role"] != "user" {
		t.Errorf("role was unexpectedly changed to %v", m["role"])
	}
}
