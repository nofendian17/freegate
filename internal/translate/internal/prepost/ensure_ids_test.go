package prepost

import (
	"encoding/json"
	"testing"
)

func TestEnsureToolCallIds(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		check    string // JSON path (jq-style) of ids to inspect after the call
		wantIDs  []string
	}{
		{
			name: "valid OpenAI id left alone",
			in: `{"messages":[
				{"role":"assistant","tool_calls":[
					{"id":"valid_id-1","type":"function","function":{"name":"f","arguments":"{}"}}
				]}
			]}`,
			wantIDs: []string{"valid_id-1"},
		},
		{
			name: "invalid chars stripped",
			in: `{"messages":[
				{"role":"assistant","tool_calls":[
					{"id":"abc!@#xyz","type":"function","function":{"name":"f","arguments":"{}"}}
				]}
			]}`,
			wantIDs: []string{"abcxyz"},
		},
		{
			name: "empty id regenerated from name",
			in: `{"messages":[
				{"role":"assistant","tool_calls":[
					{"id":"!@#","type":"function","function":{"name":"MyTool","arguments":"{}"}}
				]}
			]}`,
			wantIDs: []string{"call_msg0_tc0_MyTool"},
		},
		{
			name: "deterministic id collision-free across messages",
			in: `{"messages":[
				{"role":"assistant","tool_calls":[
					{"id":"","type":"function","function":{"name":"a","arguments":"{}"}},
					{"id":"","type":"function","function":{"name":"b","arguments":"{}"}}
				]},
				{"role":"assistant","tool_calls":[
					{"id":"","type":"function","function":{"name":"c","arguments":"{}"}}
				]}
			]}`,
			wantIDs: []string{"call_msg0_tc0_a", "call_msg0_tc1_b", "call_msg1_tc0_c"},
		},
		{
			name: "tool message tool_call_id sanitized",
			in: `{"messages":[
				{"role":"tool","tool_call_id":"bad/id","content":"x"}
			]}`,
			wantIDs: []string{"badid"},
		},
		{
			name: "claude tool_use id sanitized",
			in: `{"messages":[
				{"role":"assistant","content":[
					{"type":"tool_use","id":"bad id","name":"f","input":{}}
				]}
			]}`,
			wantIDs: []string{"badid"},
		},
		{
			name: "claude tool_result tool_use_id sanitized",
			in: `{"messages":[
				{"role":"user","content":[
					{"type":"tool_result","tool_use_id":"foo/bar","content":"ok"}
				]}
			]}`,
			wantIDs: []string{"foobar"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := EnsureToolCallIds([]byte(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := extractAllIDs(t, out)
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("got %d ids (%v), want %d (%v)", len(got), got, len(tc.wantIDs), tc.wantIDs)
			}
			for i, id := range got {
				if id != tc.wantIDs[i] {
					t.Errorf("id[%d]=%q want %q", i, id, tc.wantIDs[i])
				}
			}
		})
	}
}

func TestEnsureToolCallIds_Passthrough(t *testing.T) {
	out, err := EnsureToolCallIds(nil)
	if err != nil || out != nil {
		t.Errorf("empty body should passthrough, got out=%q err=%v", out, err)
	}
	// No messages field: should return original body
	in := []byte(`{"model":"x"}`)
	out, err = EnsureToolCallIds(in)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("no-messages body should passthrough unchanged")
	}
}

// extractAllIDs walks every message and collects every tool-related id
// in document order: tool_calls[].id, then role:"tool" tool_call_id,
// then content[].type:"tool_use" id, then content[].type:"tool_result" tool_use_id.
func extractAllIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var raw struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	ids := []string{}
	for _, m := range raw.Messages {
		if tcs, ok := m["tool_calls"].([]any); ok {
			for _, tcAny := range tcs {
				tc, _ := tcAny.(map[string]any)
				if id, ok := tc["id"].(string); ok {
					ids = append(ids, id)
				}
			}
		}
		if id, ok := m["tool_call_id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
		if content, ok := m["content"].([]any); ok {
			for _, pAny := range content {
				p, _ := pAny.(map[string]any)
				if p == nil {
					continue
				}
				switch p["type"] {
				case "tool_use":
					if id, ok := p["id"].(string); ok {
						ids = append(ids, id)
					}
				case "tool_result":
					if id, ok := p["tool_use_id"].(string); ok {
						ids = append(ids, id)
					}
				}
			}
		}
	}
	return ids
}
