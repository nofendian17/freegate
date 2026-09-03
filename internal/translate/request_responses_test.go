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

func TestRequest_OpenAIToResponses_CustomTool(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.2-contributor-free","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"custom","name":"exec","description":"run shell","format":{"syntax":"test","definition":"def"}}]}`)
	out, err := Request(body, FormatOpenAI, FormatOpenAIResponses)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	tools, _ := got["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("expected tools, got none: %s", string(out))
	}
	found := false
	for _, tr := range tools {
		if m, ok := tr.(map[string]any); ok {
			if m["name"] == "exec" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("expected exec tool, got %s", string(out))
	}
}

func TestRequest_OpenAIToResponses_PassthroughFields(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.2-contributor-free","messages":[{"role":"user","content":"hi"}],"parallel_tool_calls":false,"truncation":"auto","prompt_cache_key":"test123"}`)
	out, err := Request(body, FormatOpenAI, FormatOpenAIResponses)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["parallel_tool_calls"] != false {
		t.Errorf("expected parallel_tool_calls false preserved, got %v (%s)", got["parallel_tool_calls"], string(out))
	}
	if got["truncation"] != "auto" {
		t.Errorf("expected truncation auto, got %v", got["truncation"])
	}
	if got["prompt_cache_key"] != "test123" {
		t.Errorf("expected prompt_cache_key test123, got %v", got["prompt_cache_key"])
	}
}

func TestRequest_ResponsesToOpenAI_EncryptedContent(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.2-contributor-free","input":[{"type":"reasoning","encrypted_content":"enc123","summary":[{"type":"summary_text","text":"think"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"instructions":"sys"}`)
	out, err := Request(body, FormatOpenAIResponses, FormatOpenAI)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	msgs, _ := got["messages"].([]any)
	found := false
	for _, m := range msgs {
		if pm, ok := m.(map[string]any); ok {
			if pm["role"] == "assistant" {
				if pm["reasoning_content"] != nil || pm["encrypted_content"] != nil {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("expected reasoning_content/encrypted_content in assistant message, got %s", string(out))
	}
}
