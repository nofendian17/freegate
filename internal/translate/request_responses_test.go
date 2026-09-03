package translate

import (
	"encoding/json"
	"testing"
)

func TestRequest_OpenAIToResponses_Basic(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.2-contributor-free","messages":[{"role":"user","content":"hello"}]}`)
	out, err := Request(body, FormatOpenAI, FormatOpenAIResponses)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if _, ok := got["input"]; !ok {
		t.Errorf("expected input field in responses, got %s", string(out))
	}
	if _, ok := got["messages"]; ok {
		t.Errorf("expected no messages field after translation, got %s", string(out))
	}
	if got["model"] != "muse-spark-1.2-contributor-free" {
		t.Errorf("model mismatch")
	}
}

func TestRequest_ResponsesToOpenAI_Basic(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.2-contributor-free","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"instructions":"you are helpful"}`)
	out, err := Request(body, FormatOpenAIResponses, FormatOpenAI)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if _, ok := got["messages"]; !ok {
		t.Errorf("expected messages field after translation, got %s", string(out))
	}
	if _, ok := got["input"]; ok {
		t.Errorf("expected no input field after translation, got %s", string(out))
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("expected 2 messages (system+user), got %d: %s", len(msgs), string(out))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("first message should be system, got %v", first["role"])
	}
}

func TestRequest_OpenAIToResponses_ToolCalls(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.2-contributor-free","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_123","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]},{"role":"tool","tool_call_id":"call_123","content":"result"}]}`)
	out, err := Request(body, FormatOpenAI, FormatOpenAIResponses)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	input, _ := got["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("expected 3 input items (user, function_call, function_call_output), got %d: %s", len(input), string(out))
	}
	second, _ := input[1].(map[string]any)
	if second["type"] != "function_call" {
		t.Errorf("second item should be function_call, got %v", second["type"])
	}
}
