package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

// Responses carrying a reasoning summary must surface as a Claude thinking
// block after the Responses → OpenAI → Claude 2-hop. Pinned because the
// live muse-spark upstream intermittently returns empty summaries, making
// this path untestable against production on demand.
func TestResponseJSON_ResponsesToClaude_ReasoningBecomesThinking(t *testing.T) {
	in := []byte(`{"id":"resp_1","object":"response","model":"muse-spark","status":"completed","output":[
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"because reasons"}]},
		{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"42"}]}
	]}`)
	out, err := ResponseJSON(in, FormatOpenAIResponses, FormatClaude)
	if err != nil {
		t.Fatalf("ResponseJSON: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	content, _ := body["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content blocks=%s, want [thinking text]", string(out))
	}
	th, _ := content[0].(map[string]any)
	if th["type"] != "thinking" || th["thinking"] != "because reasons" {
		t.Errorf("first block=%v, want thinking{thinking:because reasons}", th)
	}
	txt, _ := content[1].(map[string]any)
	if txt["type"] != "text" || txt["text"] != "42" {
		t.Errorf("second block=%v, want text{42}", txt)
	}
}

// The single canonical `reasoning` field (never reasoning_content) must
// be enough input for the thinking block.
func TestResponseJSON_OpenAIToClaude_ReasoningBecomesThinking(t *testing.T) {
	in := []byte(`{"choices":[{"message":{"role":"assistant","content":"hi","reasoning":"why"},"finish_reason":"stop"}]}`)
	out, err := ResponseJSON(in, FormatOpenAI, FormatClaude)
	if err != nil {
		t.Fatalf("ResponseJSON: %v", err)
	}
	if !strings.Contains(string(out), `"type":"thinking"`) {
		t.Errorf("expected thinking block, got %s", out)
	}
	if !strings.Contains(string(out), "why") {
		t.Errorf("expected reasoning text in thinking block, got %s", out)
	}
}
