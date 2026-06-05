package prepost

import (
	"encoding/json"
	"testing"
)

func TestSanitizeToolHistory(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantRoles []string // role of each message after sanitization
		wantChanged bool   // true if the body should differ from input
	}{
		{
			name: "leading orphaned tool messages are stripped",
			in: `{"messages":[
				{"role":"tool","tool_call_id":"a1","content":"result"},
				{"role":"tool","tool_call_id":"a2","content":"result2"},
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"hello"}
			]}`,
			wantRoles:   []string{"user", "assistant"},
			wantChanged: true,
		},
		{
			name: "leading claude-shaped tool_result user messages stripped",
			in: `{"messages":[
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"ok"}]},
				{"role":"user","content":"real question"},
				{"role":"assistant","content":"answer"}
			]}`,
			wantRoles:   []string{"user", "assistant"},
			wantChanged: true,
		},
		{
			name: "trailing orphaned tool_calls stripped from last assistant",
			in: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"thinking...","tool_calls":[{"id":"a1","type":"function","function":{"name":"f","arguments":"{}"}}]}
			]}`,
			wantRoles:   []string{"user", "assistant"},
			wantChanged: true,
		},
		{
			name: "trailing tool_calls with responses are preserved",
			in: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"","tool_calls":[{"id":"a1","type":"function","function":{"name":"f","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"a1","content":"ok"}
			]}`,
			wantRoles:   []string{"user", "assistant", "tool"},
			wantChanged: false,
		},
		{
			name: "normal conversation without tools unchanged",
			in: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"hello"}
			]}`,
			wantRoles:   []string{"user", "assistant"},
			wantChanged: false,
		},
		{
			name: "both leading and trailing orphans cleaned",
			in: `{"messages":[
				{"role":"tool","tool_call_id":"old","content":"stale"},
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"let me check","tool_calls":[{"id":"a1","type":"function","function":{"name":"f","arguments":"{}"}}]}
			]}`,
			wantRoles:   []string{"user", "assistant"},
			wantChanged: true,
		},
		{
			name: "trailing assistant without tool_calls is untouched",
			in: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"hello"},
				{"role":"user","content":"bye"},
				{"role":"assistant","content":"goodbye"}
			]}`,
			wantRoles:   []string{"user", "assistant", "user", "assistant"},
			wantChanged: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := SanitizeToolHistory([]byte(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var got struct {
				Messages []map[string]any `json:"messages"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("invalid JSON output: %v", err)
			}

			if len(got.Messages) != len(tc.wantRoles) {
				t.Fatalf("got %d messages, want %d (output=%s)", len(got.Messages), len(tc.wantRoles), out)
			}
			for i, m := range got.Messages {
				if role, _ := m["role"].(string); role != tc.wantRoles[i] {
					t.Errorf("msg[%d] role=%q want %q", i, role, tc.wantRoles[i])
				}
			}

			// Verify changed flag
			inputNorm := normalizeJSON(t, tc.in)
			outputNorm := normalizeJSON(t, string(out))
			actualChanged := inputNorm != outputNorm
			if actualChanged != tc.wantChanged {
				t.Errorf("changed=%v want %v", actualChanged, tc.wantChanged)
			}
		})
	}
}

func TestSanitizeToolHistory_TrailingToolCallsContent(t *testing.T) {
	// When trailing tool_calls are stripped, the assistant message's
	// text content should be preserved.
	in := `{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"I'll check that for you","tool_calls":[{"id":"a1","type":"function","function":{"name":"f","arguments":"{}"}}]}
	]}`

	out, err := SanitizeToolHistory([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got.Messages))
	}

	last := got.Messages[1]
	// tool_calls should be gone
	if _, ok := last["tool_calls"]; ok {
		t.Error("tool_calls should have been removed from the last assistant message")
	}
	// text content should be preserved
	content, _ := last["content"].(string)
	if content != "I'll check that for you" {
		t.Errorf("content=%q, want %q", content, "I'll check that for you")
	}
}

func TestSanitizeToolHistory_Passthrough(t *testing.T) {
	// Empty body
	out, err := SanitizeToolHistory(nil)
	if err != nil || out != nil {
		t.Errorf("empty body should passthrough, got out=%q err=%v", out, err)
	}
	// No messages field
	out, err = SanitizeToolHistory([]byte(`{"model":"x"}`))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if string(out) != `{"model":"x"}` {
		t.Errorf("no-messages body should passthrough unchanged, got %s", out)
	}
}

func normalizeJSON(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("normalizeJSON: %v", err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
