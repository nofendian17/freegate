package responses

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStream_ResponsesParallelToolCallsKeepIndexes(t *testing.T) {
	// Regression: muse-spark fans out parallel function_calls interleaved
	// (added 2, added 3, delta 2, delta 3, ...) without done in between.
	// The old single-counter mapping collapsed all deltas onto index 0,
	// producing `{"cmd":"a"}{"cmd":"b"}...` — upstream rejects with
	// "`arguments` must be valid JSON" (seen 2026-09-20, request b357).
	s := NewStreamState()
	var chunks []string
	emit := func(ev string, data map[string]any) {
		chunks = append(chunks, s.ResponsesEventToOpenAI(ev, data)...)
	}
	ids := []string{"fc_aaa", "fc_bbb", "fc_ccc", "fc_ddd"}
	calls := []string{"call_aaa", "call_bbb", "call_ccc", "call_ddd"}
	deltas := []string{`{"cmd":"one"}`, `{"cmd":"two"}`, `{"cmd":"three"}`, `{"cmd":"four"}`}
	for i := range ids {
		emit("response.output_item.added", map[string]any{
			"output_index": float64(2 + i),
			"item":         map[string]any{"id": ids[i], "type": "function_call", "call_id": calls[i], "name": "exec_command"},
		})
	}
	for i := range ids {
		emit("response.function_call_arguments.delta", map[string]any{
			"output_index": float64(2 + i),
			"item_id":      ids[i],
			"delta":        deltas[i],
		})
	}
	byIdx := map[int][]string{}
	for _, c := range chunks {
		line := strings.Split(c, "\n")[0]
		body, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			continue
		}
		choices, _ := m["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		ch, _ := choices[0].(map[string]any)
		d, _ := ch["delta"].(map[string]any)
		tcs, _ := d["tool_calls"].([]any)
		for _, tcRaw := range tcs {
			tc, _ := tcRaw.(map[string]any)
			idx := 0
			if v, ok := tc["index"].(float64); ok {
				idx = int(v)
			}
			if fn, ok := tc["function"].(map[string]any); ok {
				if a, ok := fn["arguments"].(string); ok && a != "" {
					byIdx[idx] = append(byIdx[idx], a)
				}
			}
		}
	}
	if len(byIdx) != 4 {
		t.Fatalf("distinct tool indexes=%d (%v), want 4 — deltas merged", len(byIdx), byIdx)
	}
	for idx, parts := range byIdx {
		joined := strings.Join(parts, "")
		var js any
		if err := json.Unmarshal([]byte(joined), &js); err != nil {
			t.Errorf("index %d arguments invalid JSON: %q: %v", idx, joined, err)
		}
	}
}
