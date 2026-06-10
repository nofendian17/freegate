package prepost

import (
	"encoding/json"
	"testing"
)

func TestNormalizeRequestReasoning_CopiesReasoningToRC(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"hi","reasoning":"think"}]}`)
	out, err := NormalizeRequestReasoning(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]any
	json.Unmarshal(out, &raw)
	msgs := raw["messages"].([]any)
	msg := msgs[0].(map[string]any)
	if msg["reasoning_content"] != "think" {
		t.Errorf("expected reasoning_content='think', got %v", msg["reasoning_content"])
	}
}

func TestNormalizeRequestReasoning_PreservesExistingRC(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"hi","reasoning":"a","reasoning_content":"b"}]}`)
	out, err := NormalizeRequestReasoning(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]any
	json.Unmarshal(out, &raw)
	msgs := raw["messages"].([]any)
	msg := msgs[0].(map[string]any)
	if msg["reasoning_content"] != "b" {
		t.Errorf("expected reasoning_content='b' (not overwritten), got %v", msg["reasoning_content"])
	}
}

func TestNormalizeRequestReasoning_NoOpWhenNoReasoning(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"hi"}]}`)
	out, err := NormalizeRequestReasoning(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("expected body unchanged, got %s", out)
	}
}

func TestNormalizeRequestReasoning_OnlyAssistantMessages(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"q","reasoning":"noise"}]}`)
	out, err := NormalizeRequestReasoning(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]any
	json.Unmarshal(out, &raw)
	msgs := raw["messages"].([]any)
	msg := msgs[0].(map[string]any)
	if _, ok := msg["reasoning_content"]; ok {
		t.Error("expected reasoning_content to NOT be set for user messages")
	}
}
