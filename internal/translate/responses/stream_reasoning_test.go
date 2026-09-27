package responses

import (
	"encoding/json"
	"strings"
	"testing"
)

// Reasoning deltas normalize to the single canonical `delta.reasoning`
// field (reasoning_content is DeepSeek-only and stripped everywhere).
func TestStream_ResponsesToOpenAI_ReasoningDeltaEmitsOnlyReasoning(t *testing.T) {
	s := NewStreamState()
	events := s.ResponsesEventToOpenAI("response.reasoning_summary_text.delta", map[string]any{
		"item_id": "rs_1", "output_index": float64(0), "summary_index": float64(0),
		"delta": "thinking about it",
	})
	if len(events) == 0 {
		t.Fatal("no chunk emitted for reasoning_summary_text.delta")
	}

	body, ok := strings.CutPrefix(strings.Split(events[0], "\n")[0], "data: ")
	if !ok {
		t.Fatalf("not an SSE data line: %q", events[0])
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(body), &chunk); err != nil {
		t.Fatalf("unmarshal chunk: %v", err)
	}
	choices, _ := chunk["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices=%v, want 1", chunk["choices"])
	}
	delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
	if delta == nil {
		t.Fatalf("no delta object in %s", body)
	}

	r, _ := delta["reasoning"].(string)
	if r != "thinking about it" {
		t.Errorf("reasoning=%q, want %q", r, "thinking about it")
	}
	if _, ok := delta["reasoning_content"]; ok {
		t.Errorf("reasoning_content must be stripped, got %v", delta["reasoning_content"])
	}
}

// The emitted OpenAI chunks must echo the request's model, not the
// hardcoded family placeholder — any model routed via Responses (custom
// RESPONSE_MODELS entries, raw /v1/responses passthrough) hits this path.
func TestStream_ResponsesToOpenAI_EchoesRequestModel(t *testing.T) {
	s := NewStreamState()
	s.SetModel("custom-model-free")
	events := s.ResponsesEventToOpenAI("response.output_text.delta", map[string]any{
		"delta": "hi",
	})
	if len(events) == 0 {
		t.Fatal("no chunk emitted for output_text.delta")
	}
	body, ok := strings.CutPrefix(strings.Split(events[0], "\n")[0], "data: ")
	if !ok {
		t.Fatalf("not an SSE data line: %q", events[0])
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(body), &chunk); err != nil {
		t.Fatalf("unmarshal chunk: %v", err)
	}
	if chunk["model"] != "custom-model-free" {
		t.Errorf("model=%v, want %q", chunk["model"], "custom-model-free")
	}
}

// Without a threaded model the family placeholder is kept so old
// unit tests and unknown paths still emit something plausible.
func TestStream_ResponsesToOpenAI_ModelFallback(t *testing.T) {
	s := NewStreamState()
	events := s.ResponsesEventToOpenAI("response.output_text.delta", map[string]any{
		"delta": "hi",
	})
	if len(events) == 0 {
		t.Fatal("no chunk emitted for output_text.delta")
	}
	if !strings.Contains(events[0], `"model":"muse-spark"`) {
		t.Errorf("expected muse-spark fallback, got %q", events[0])
	}
}

// Parity guard: the non-streaming direction must also emit only the
// single canonical key.
func TestStream_NonStreamingMessageEmitsOnlyReasoning(t *testing.T) {
	in := []byte(`{"id":"resp_1","output":[
		{"type":"reasoning","summary":[{"type":"summary_text","text":"because"}]},
		{"type":"message","content":[{"type":"output_text","text":"42"}]}
	]}`)
	out, err := JSONToOpenAI(in)
	if err != nil {
		t.Fatalf("JSONToOpenAI: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	choices, _ := body["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices=%v, want 1", body["choices"])
	}
	msg, _ := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["reasoning"] != "because" {
		t.Errorf("reasoning=%v, want %q", msg["reasoning"], "because")
	}
	if _, ok := msg["reasoning_content"]; ok {
		t.Errorf("reasoning_content must be stripped, got %v", msg["reasoning_content"])
	}
}
