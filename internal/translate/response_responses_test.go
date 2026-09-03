package translate

import (
	"encoding/json"
	"testing"
)

func TestResponseJSON_ChatToResponses(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-123","object":"chat.completion","created":1700000000,"model":"muse-spark-1.2-contributor-free","choices":[{"index":0,"message":{"role":"assistant","content":"hello world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	out, err := ResponseJSON(body, FormatOpenAI, FormatOpenAIResponses)
	if err != nil {
		t.Fatalf("ResponseJSON failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got["object"] != "response" {
		t.Errorf("expected response object, got %v (%s)", got["object"], string(out))
	}
	if _, ok := got["output"]; !ok {
		t.Errorf("expected output field, got %s", string(out))
	}
}

func TestResponseJSON_ResponsesToChat(t *testing.T) {
	body := []byte(`{"id":"resp_123","object":"response","created_at":1700000000,"model":"muse-spark-1.2-contributor-free","status":"completed","output":[{"type":"message","id":"msg_0","role":"assistant","content":[{"type":"output_text","text":"hello from muse"}]}]}`)
	out, err := ResponseJSON(body, FormatOpenAIResponses, FormatOpenAI)
	if err != nil {
		t.Fatalf("ResponseJSON failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got["object"] != "chat.completion" {
		t.Errorf("expected chat.completion, got %v (%s)", got["object"], string(out))
	}
	if _, ok := got["choices"]; !ok {
		t.Errorf("expected choices field, got %s", string(out))
	}
}
