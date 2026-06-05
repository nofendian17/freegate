package prepost

import (
	"encoding/json"
	"testing"
)

func TestPrepareClaudeRequest_CacheControl(t *testing.T) {
	in := `{
		"system":[
			{"type":"text","text":"a","cache_control":{"type":"ephemeral"}},
			{"type":"text","text":"b","cache_control":{"type":"ephemeral","ttl":"5m"}}
		],
		"tools":[
			{"name":"a","description":"","input_schema":{},"cache_control":{"type":"ephemeral"}},
			{"name":"b","description":"","input_schema":{},"cache_control":{"type":"ephemeral"}}
		],
		"messages":[{"role":"user","content":"hi"}]
	}`
	out, err := PrepareClaudeRequest([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// System: only last block has cache_control with ttl=1h
	sys, _ := got["system"].([]any)
	if len(sys) != 2 {
		t.Fatalf("expected 2 system blocks, got %d", len(sys))
	}
	first, _ := sys[0].(map[string]any)
	if _, has := first["cache_control"]; has {
		t.Errorf("first system block should not have cache_control")
	}
	last, _ := sys[1].(map[string]any)
	cc, ok := last["cache_control"].(map[string]any)
	if !ok {
		t.Fatalf("last system block missing cache_control")
	}
	if cc["ttl"] != "1h" {
		t.Errorf("last system block ttl=%v want 1h", cc["ttl"])
	}
	// Tools: only last tool has cache_control
	tools, _ := got["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	firstTool, _ := tools[0].(map[string]any)
	if _, has := firstTool["cache_control"]; has {
		t.Errorf("first tool should not have cache_control")
	}
	lastTool, _ := tools[1].(map[string]any)
	cc, ok = lastTool["cache_control"].(map[string]any)
	if !ok {
		t.Fatalf("last tool missing cache_control")
	}
	if cc["ttl"] != "1h" {
		t.Errorf("last tool ttl=%v want 1h", cc["ttl"])
	}
}

func TestPrepareClaudeRequest_DropEmptyMessages(t *testing.T) {
	in := `{
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":""},
			{"role":"user","content":""},
			{"role":"assistant","content":"final"}
		]
	}`
	out, err := PrepareClaudeRequest([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// Expect: [user "hi", assistant "" (kept because final), but "user" is empty
	//         so the empty ones in the middle drop, but final assistant always kept]
	// After drop: [user "hi", assistant "final"]
	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d (%+v)", len(got.Messages), got.Messages)
	}
}

func TestPrepareClaudeRequest_KeepFinalAssistant(t *testing.T) {
	in := `{
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":""}
		]
	}`
	out, err := PrepareClaudeRequest([]byte(in))
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
		t.Fatalf("expected 2 messages (final assistant kept), got %d", len(got.Messages))
	}
}

func TestPrepareClaudeRequest_FixToolUseOrdering(t *testing.T) {
	in := `{
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"hmm"},
				{"type":"text","text":"before"},
				{"type":"tool_use","id":"u1","name":"f","input":{}},
				{"type":"text","text":"after-should-drop"}
			]}
		]
	}`
	out, err := PrepareClaudeRequest([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got.Messages))
	}
	blocks := got.Messages[0].Content
	// Expected: thinking, text "before", tool_use
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks (thinking + before-text + tool_use), got %d (%+v)", len(blocks), blocks)
	}
	if blocks[0]["type"] != "thinking" {
		t.Errorf("blocks[0] type=%v want thinking", blocks[0]["type"])
	}
	if blocks[1]["type"] != "text" || blocks[1]["text"] != "before" {
		t.Errorf("blocks[1] should be text 'before', got %+v", blocks[1])
	}
	if blocks[2]["type"] != "tool_use" {
		t.Errorf("blocks[2] should be tool_use, got %+v", blocks[2])
	}
}

func TestPrepareClaudeRequest_MergeConsecutiveSameRole(t *testing.T) {
	in := `{
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"hello"}
			]},
			{"role":"user","content":[
				{"type":"text","text":"world"},
				{"type":"tool_result","tool_use_id":"u1","content":"ok"}
			]}
		]
	}`
	out, err := PrepareClaudeRequest([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("expected 1 merged message, got %d", len(got.Messages))
	}
	blocks := got.Messages[0].Content
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks after merge, got %d (%+v)", len(blocks), blocks)
	}
	// First should be the tool_result (per mergeInto ordering)
	if blocks[0]["type"] != "tool_result" {
		t.Errorf("merged content[0] should be tool_result, got %+v", blocks[0])
	}
}

func TestPrepareClaudeRequest_Passthrough(t *testing.T) {
	out, err := PrepareClaudeRequest(nil)
	if err != nil || out != nil {
		t.Errorf("empty body should passthrough, got out=%q err=%v", out, err)
	}
}

func TestPrepareClaudeRequest_StringSystem(t *testing.T) {
	// String system is left alone.
	in := `{"system":"you are helpful","messages":[{"role":"user","content":"hi"}]}`
	out, err := PrepareClaudeRequest([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if sys, _ := got["system"].(string); sys != "you are helpful" {
		t.Errorf("string system should be untouched, got %v", got["system"])
	}
}
