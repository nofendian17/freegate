package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestProcessChunk_TextInterleavedMidToolCallPreservesArgs reproduces the
// Claude Code failure "The required parameter `command` is missing" caused
// by interleaved content: the model streams a partial tool-call argument,
// then text, then the rest of the arguments. closeOpenToolBlocks used to
// discard the buffered arguments without emitting them and reset the maps,
// so the tool_use block was closed with empty input and late fragments were
// dropped.
//
// Contract: all argument fragments accumulate into one repaired
// input_json_delta emitted BEFORE the tool block's content_block_stop, and
// the interleaved text still reaches the client.
func TestProcessChunk_TextInterleavedMidToolCallPreservesArgs(t *testing.T) {
	state := NewStreamState()

	feed := func(delta map[string]any) []string {
		return ProcessChunk(map[string]any{
			"choices": []any{map[string]any{
				"index":         0.0,
				"delta":         delta,
				"finish_reason": nil,
			}},
		}, state)
	}

	var events []string
	events = append(events, feed(map[string]any{
		"tool_calls": []any{map[string]any{
			"index": 0.0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "Bash", "arguments": `{"command":`},
		}},
	})...)
	events = append(events, feed(map[string]any{"content": "running ls"})...)
	events = append(events, feed(map[string]any{
		"tool_calls": []any{map[string]any{
			"index":    0.0,
			"function": map[string]any{"arguments": `"ls -la"}`},
		}},
	})...)
	events = append(events, ProcessChunk(map[string]any{
		"choices": []any{map[string]any{
			"index":         0.0,
			"delta":         map[string]any{},
			"finish_reason": "tool_calls",
		}},
	}, state)...)

	const wantArgs = `{"command":"ls -la"}`
	partialByIndex := map[int]string{}
	stopCount := map[int]int{}
	sawText := false

	for _, e := range events {
		for _, line := range strings.Split(e, "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var evt struct {
				Type  string `json:"type"`
				Index *int   `json:"index"`
				Delta *struct {
					Type        string `json:"type"`
					PartialJSON string `json:"partial_json"`
					Text        string `json:"text"`
				} `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &evt) != nil {
				continue
			}
			switch evt.Type {
			case "content_block_delta":
				if evt.Delta == nil {
					continue
				}
				idx := -1
				if evt.Index != nil {
					idx = *evt.Index
				}
				switch evt.Delta.Type {
				case "input_json_delta":
					partialByIndex[idx] += evt.Delta.PartialJSON
				case "text_delta":
					if strings.Contains(evt.Delta.Text, "running ls") {
						sawText = true
					}
				}
			case "content_block_stop":
				idx := -1
				if evt.Index != nil {
					idx = *evt.Index
				}
				stopCount[idx]++
			}
		}
	}

	foundArgs := false
	for _, p := range partialByIndex {
		if strings.TrimSpace(p) == wantArgs {
			foundArgs = true
		}
	}
	if !foundArgs {
		t.Fatalf("expected accumulated args %s in an input_json_delta, got %v\nevents:\n%s",
			wantArgs, partialByIndex, strings.Join(events, ""))
	}

	for idx, n := range stopCount {
		if n > 1 {
			t.Fatalf("content_block_stop emitted %d times for block index %d (want at most 1)", n, idx)
		}
	}

	if !sawText {
		t.Fatalf("interleaved text was lost; expected it after the tool block")
	}
}
