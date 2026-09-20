package responses

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONToResponses_PreservesEncryptedContent(t *testing.T) {
	body := `{"id":"chatcmpl-1","created":1,"model":"x","choices":
		[{"message":{"reasoning_content":"thinking","content":"hi","encrypted_content":"enc-blob"}}],
		"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	out, err := JSONToResponses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	output := raw["output"].([]any)
	if len(output) == 0 {
		t.Fatal("expected output items")
	}
	reasoning := output[0].(map[string]any)
	if reasoning["type"] != "reasoning" {
		t.Fatalf("type=%v, want reasoning", reasoning["type"])
	}
	if enc, _ := reasoning["encrypted_content"].(string); enc != "enc-blob" {
		t.Fatalf("encrypted_content=%q, want enc-blob", enc)
	}
	if len(reasoning["summary"].([]any)) != 1 {
		t.Error("summary must be preserved alongside encrypted content")
	}
}

func TestJSONToResponses_EncryptedOnlyCreatesItem(t *testing.T) {
	body := `{"id":"chatcmpl-1","created":1,"model":"x","choices":
		[{"message":{"content":"hi","encrypted_content":"enc-only"}}]}`
	out, err := JSONToResponses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range raw["output"].([]any) {
		m := it.(map[string]any)
		if m["type"] == "reasoning" {
			found = true
			if enc, _ := m["encrypted_content"].(string); enc != "enc-only" {
				t.Fatalf("encrypted_content=%q, want enc-only", enc)
			}
		}
	}
	if !found {
		t.Fatal("encrypted-only message must still produce a reasoning item")
	}
}

func TestJSONToResponses_NoEncryptedStaysUnchanged(t *testing.T) {
	body := `{"id":"chatcmpl-1","created":1,"model":"x","choices":
		[{"message":{"reasoning_content":"thinking","content":"hi"}}]}`
	out, err := JSONToResponses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "encrypted_content") {
		t.Error("no encrypted key must appear when upstream sent none")
	}
}

func TestStream_EncryptedContentAccumulatesToDone(t *testing.T) {
	s := NewStreamState()
	var events []string
	feed := func(delta map[string]any, finish string) {
		chunk := map[string]any{
			"id":      "chatcmpl-1",
			"created": float64(1),
			"choices": []any{map[string]any{
				"index":         float64(0),
				"delta":         delta,
				"finish_reason": finish,
			}},
		}
		events = append(events, s.OpenAIChunkToResponses(chunk)...)
	}
	feed(map[string]any{"reasoning_content": "think"}, "")
	feed(map[string]any{"encrypted_content": "enc-a"}, "")
	feed(map[string]any{"encrypted_content": "enc-b"}, "")
	feed(map[string]any{"content": "hi"}, "stop")

	var done map[string]any
	for _, e := range events {
		lines := strings.SplitN(e, "data: ", 2)
		if len(lines) != 2 {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(lines[1]), &ev); err != nil {
			continue
		}
		if ev["type"] == "response.output_item.done" {
			if item, ok := ev["item"].(map[string]any); ok && item["type"] == "reasoning" {
				done = item
			}
		}
	}
	if done == nil {
		t.Fatal("no reasoning done item emitted")
	}
	if enc, _ := done["encrypted_content"].(string); enc != "enc-aenc-b" {
		t.Fatalf("encrypted_content=%q, want concatenation enc-aenc-b", enc)
	}
}

func TestStream_NoEncryptedStaysUnchanged(t *testing.T) {
	s := NewStreamState()
	chunk := map[string]any{
		"id":      "chatcmpl-1",
		"created": float64(1),
		"choices": []any{map[string]any{
			"index":         float64(0),
			"delta":         map[string]any{"reasoning_content": "think", "content": "hi"},
			"finish_reason": "stop",
		}},
	}
	var joined string
	for _, e := range s.OpenAIChunkToResponses(chunk) {
		joined += e
	}
	if strings.Contains(joined, "encrypted_content") {
		t.Error("no encrypted key must appear when upstream sent none")
	}
}
