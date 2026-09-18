package prepost

import (
	"encoding/json"
	"testing"
)

func TestNormalizeClaudeContent_StripsEmptyTextBlocks(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":""},{"type":"text","text":"hi"}]},
		{"role":"assistant","content":[{"type":"text","text":"ok"}]}
	]}`)
	out, applied, err := NormalizeClaudeContent(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(applied) != 1 || applied[0] != AppliedClaudeStripEmpty {
		t.Errorf("expected tokens [%q], got %v", AppliedClaudeStripEmpty, applied)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	msgs := raw["messages"].([]any)
	parts := msgs[0].(map[string]any)["content"].([]any)
	if len(parts) != 1 {
		t.Fatalf("expected 1 block, got %d (%s)", len(parts), out)
	}
	if parts[0].(map[string]any)["text"] != "hi" {
		t.Errorf("expected surviving text='hi', got %v", parts[0])
	}
}

func TestNormalizeClaudeContent_ThinkingBlocks(t *testing.T) {
	tests := []struct {
		name     string
		block    string
		stripped bool
	}{
		{name: "unsigned empty thinking stripped", block: `{"type":"thinking","thinking":""}`, stripped: true},
		{name: "missing fields stripped", block: `{"type":"thinking"}`, stripped: true},
		{name: "signed empty thinking kept", block: `{"type":"thinking","thinking":"","signature":"sig"}`, stripped: false},
		{name: "unsigned nonempty thinking kept", block: `{"type":"thinking","thinking":"hmm"}`, stripped: false},
		{name: "signed thinking kept", block: `{"type":"thinking","thinking":"hmm","signature":"sig"}`, stripped: false},
		{name: "empty redacted thinking stripped", block: `{"type":"redacted_thinking","data":""}`, stripped: true},
		{name: "nonempty redacted thinking kept", block: `{"type":"redacted_thinking","data":"enc"}`, stripped: false},
		{name: "tool_use kept", block: `{"type":"tool_use","id":"a","name":"b","input":{}}`, stripped: false},
		{name: "tool_result kept", block: `{"type":"tool_result","tool_use_id":"a","content":""}`, stripped: false},
		{name: "image kept", block: `{"type":"image","source":{"type":"base64"}}`, stripped: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"messages":[{"role":"assistant","content":[` + tt.block + `,{"type":"text","text":"done"}]}]}`)
			out, _, err := NormalizeClaudeContent(body)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var raw map[string]any
			if err := json.Unmarshal(out, &raw); err != nil {
				t.Fatalf("invalid JSON result: %v", err)
			}
			parts := raw["messages"].([]any)[0].(map[string]any)["content"].([]any)
			want := 1
			if !tt.stripped {
				want = 2
			}
			if len(parts) != want {
				t.Errorf("expected %d blocks, got %d (%s)", want, len(parts), out)
			}
		})
	}
}

func TestNormalizeClaudeContent_DropsEmptiedMessageKeepsFinalAssistant(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":""}]},
		{"role":"user","content":"real"},
		{"role":"assistant","content":[{"type":"text","text":""}]}
	]}`)
	out, _, err := NormalizeClaudeContent(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	msgs := raw["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d (%s)", len(msgs), out)
	}
	if msgs[0].(map[string]any)["content"] != "real" {
		t.Errorf("expected first message content='real', got %v", msgs[0])
	}
	if msgs[1].(map[string]any)["role"] != "assistant" {
		t.Errorf("expected final assistant kept, got %v", msgs[1])
	}
}

func TestNormalizeClaudeContent_StringContentUntouched(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	out, _, err := NormalizeClaudeContent(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("expected body unchanged, got %s", out)
	}
}

func TestNormalizeClaudeContent_NoContentKey(t *testing.T) {
	body := []byte(`{"model":"x"}`)
	out, _, err := NormalizeClaudeContent(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("expected body unchanged, got %s", out)
	}
}

func TestNormalizeClaudeContent_Idempotent(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":""},{"type":"text","text":"hi"}]},
		{"role":"assistant","content":[{"type":"thinking","thinking":"hmm","signature":"sig"},{"type":"text","text":"ok"}]}
	]}`)
	once, _, err := NormalizeClaudeContent(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	twice, _, err := NormalizeClaudeContent(once)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(once) != string(twice) {
		t.Errorf("expected idempotent output\nfirst:  %s\nsecond: %s", once, twice)
	}
}

func TestNormalizeClaudeContent_InvalidJSON(t *testing.T) {
	if _, _, err := NormalizeClaudeContent([]byte(`{"messages":[{"role":"user","content": invalid}]}`)); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestNormalizeClaudeContent_Empty(t *testing.T) {
	out, _, err := NormalizeClaudeContent(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty output, got %s", out)
	}
}
