package prepost

import (
	"encoding/json"
	"testing"
)

func TestFixMissingToolResponses(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantRoles []string // role of each message in order after fix
		wantTool  []string // tool_call_id of each inserted tool message
	}{
		{
			name: "assistant tool_calls, no follow-up tool message",
			in: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"","tool_calls":[{"id":"a1","type":"function","function":{"name":"f","arguments":"{}"}}]}
			]}`,
			wantRoles: []string{"user", "assistant", "tool"},
			wantTool:  []string{"a1"},
		},
		{
			name: "assistant tool_calls already followed by matching tool message",
			in: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"","tool_calls":[{"id":"a1","type":"function","function":{"name":"f","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"a1","content":"ok"}
			]}`,
			wantRoles: []string{"user", "assistant", "tool"},
			wantTool:  nil,
		},
		{
			name: "assistant tool_calls, only partial follow-up — fill missing only",
			in: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"","tool_calls":[
					{"id":"a1","type":"function","function":{"name":"f","arguments":"{}"}},
					{"id":"a2","type":"function","function":{"name":"g","arguments":"{}"}}
				]},
				{"role":"tool","tool_call_id":"a1","content":"ok"}
			]}`,
			wantRoles: []string{"user", "assistant", "tool", "tool"},
			wantTool:  []string{"a2"}, // a1 already has a real response; only a2 inserted
		},
		{
			name: "multi tool_calls with consecutive tool responses all found",
			in: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"","tool_calls":[
					{"id":"a1","type":"function","function":{"name":"f","arguments":"{}"}},
					{"id":"a2","type":"function","function":{"name":"g","arguments":"{}"}},
					{"id":"a3","type":"function","function":{"name":"h","arguments":"{}"}}
				]},
				{"role":"tool","tool_call_id":"a1","content":"r1"},
				{"role":"tool","tool_call_id":"a2","content":"r2"},
				{"role":"tool","tool_call_id":"a3","content":"r3"}
			]}`,
			wantRoles: []string{"user", "assistant", "tool", "tool", "tool"},
			wantTool:  nil, // all responded, no synthetics
		},
		{
			name: "multi tool_calls with only first responded — fills rest",
			in: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"","tool_calls":[
					{"id":"a1","type":"function","function":{"name":"f","arguments":"{}"}},
					{"id":"a2","type":"function","function":{"name":"g","arguments":"{}"}},
					{"id":"a3","type":"function","function":{"name":"h","arguments":"{}"}}
				]},
				{"role":"tool","tool_call_id":"a1","content":"r1"}
			]}`,
			wantRoles: []string{"user", "assistant", "tool", "tool", "tool"},
			wantTool:  []string{"a2", "a3"},
		},
		{
			name: "no tool_calls, no fix",
			in: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"hello"}
			]}`,
			wantRoles: []string{"user", "assistant"},
			wantTool:  nil,
		},
		{
			name: "claude tool_use block answered by tool_result block",
			in: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":[
					{"type":"text","text":"thinking"},
					{"type":"tool_use","id":"u1","name":"f","input":{}}
				]},
				{"role":"user","content":[
					{"type":"tool_result","tool_use_id":"u1","content":"ok"}
				]}
			]}`,
			wantRoles: []string{"user", "assistant", "user"},
			wantTool:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := FixMissingToolResponses([]byte(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got struct {
				Messages []map[string]any `json:"messages"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if len(got.Messages) != len(tc.wantRoles) {
				t.Fatalf("got %d messages, want %d (output=%s)", len(got.Messages), len(tc.wantRoles), out)
			}
			for i, m := range got.Messages {
				if role, _ := m["role"].(string); role != tc.wantRoles[i] {
					t.Errorf("msg[%d] role=%q want %q", i, role, tc.wantRoles[i])
				}
			}
			// Inspect inserted tool messages
			inserted := []string{}
			for _, m := range got.Messages {
				if role, _ := m["role"].(string); role == "tool" {
					if id, ok := m["tool_call_id"].(string); ok && id != "" {
						// collect only the synthetic ones (content matches SyntheticToolContent)
						if c, _ := m["content"].(string); c == SyntheticToolContent {
							inserted = append(inserted, id)
						}
					}
				}
			}
			if len(inserted) != len(tc.wantTool) {
				t.Errorf("got %d inserted tool messages (%v), want %d (%v)", len(inserted), inserted, len(tc.wantTool), tc.wantTool)
			} else {
				for i, id := range inserted {
					if id != tc.wantTool[i] {
						t.Errorf("inserted[%d]=%q want %q", i, id, tc.wantTool[i])
					}
				}
			}
		})
	}
}

func TestFixMissingToolResponses_Passthrough(t *testing.T) {
	// Empty body
	out, err := FixMissingToolResponses(nil)
	if err != nil || out != nil {
		t.Errorf("empty body should passthrough, got out=%q err=%v", out, err)
	}
	// No messages field
	out, err = FixMissingToolResponses([]byte(`{"model":"x"}`))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if string(out) != `{"model":"x"}` {
		t.Errorf("no-messages body should passthrough unchanged, got %s", out)
	}
}
