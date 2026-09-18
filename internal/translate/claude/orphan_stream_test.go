package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

func orphanChunk(content string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{
		"index": 0.0,
		"delta": map[string]any{"role": "assistant", "content": content},
	}}}
}

func orphanFinish(reason string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{
		"index":         0.0,
		"delta":         map[string]any{},
		"finish_reason": reason,
	}}}
}

// feedText splits s into pieceSize deltas through ProcessChunk, then a
// finish chunk, returning all joined events.
func feedText(t *testing.T, state *StreamState, s string, pieceSize int, finish string) string {
	t.Helper()
	var events []string
	for i := 0; i < len(s); i += pieceSize {
		end := i + pieceSize
		if end > len(s) {
			end = len(s)
		}
		events = append(events, ProcessChunk(orphanChunk(s[i:end]), state)...)
	}
	events = append(events, ProcessChunk(orphanFinish(finish), state)...)
	return strings.Join(events, "\n")
}

func TestStream_OrphanInvokeSplitAcrossDeltas(t *testing.T) {
	d := "｜DSML｜"
	text := "Let me run it.\n<" + d + "invoke name=\"terminal\">\n<" + d + "parameter name=\"command\" string=\"true\">echo hi</" + d + "parameter>\n</" + d + "invoke>\n"
	state := NewStreamState()
	joined := feedText(t, state, text, 7, "stop")
	if !strings.Contains(joined, `"type":"tool_use"`) {
		t.Errorf("expected tool_use block, got:\n%s", joined)
	}
	if !strings.Contains(joined, `"name":"terminal"`) {
		t.Errorf("expected tool name terminal, got:\n%s", joined)
	}
	if !strings.Contains(joined, `echo hi`) {
		t.Errorf("expected args forwarded, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Let me ") || !strings.Contains(joined, "run it.") {
		t.Errorf("expected leading prose, got:\n%s", joined)
	}
	if strings.Contains(joined, "DSML") {
		t.Errorf("DSML leaked to client, got:\n%s", joined)
	}
	// Turn ended in a tool call: stop reason must be tool_use, not end_turn.
	if !strings.Contains(joined, `"stop_reason":"tool_use"`) {
		t.Errorf("expected stop_reason tool_use, got:\n%s", joined)
	}
}

func TestStream_OrphanInvokeTrailingDropped(t *testing.T) {
	d := "｜DSML｜"
	text := "pre\n<" + d + "invoke name=\"terminal\">\n<" + d + "parameter name=\"command\" string=\"true\">echo hi</" + d + "parameter>\n</" + d + "invoke>\nDone."
	state := NewStreamState()
	joined := feedText(t, state, text, 11, "stop")
	// Concatenate text deltas: trailing prose must be dropped even when
	// split across events (a tool call ends the turn, per the official fix).
	var prose strings.Builder
	for _, line := range strings.Split(joined, "\n") {
		if !strings.Contains(line, "text_delta") {
			continue
		}
		start := strings.Index(line, "{")
		var evt map[string]any
		if start >= 0 && json.Unmarshal([]byte(line[start:]), &evt) == nil {
			if delta, _ := evt["delta"].(map[string]any); delta != nil {
				prose.WriteString(delta["text"].(string))
			}
		}
	}
	if strings.Contains(prose.String(), "Done.") {
		t.Errorf("expected trailing prose dropped, got prose %q", prose.String())
	}
	if !strings.Contains(prose.String(), "pre") {
		t.Errorf("expected leading prose kept, got %q", prose.String())
	}
	if !strings.Contains(joined, `"name":"terminal"`) {
		t.Errorf("expected tool recovered, got:\n%s", joined)
	}
}

func TestStream_NativeToolCallsWinOverOrphan(t *testing.T) {
	state := NewStreamState()
	events := ProcessChunk(map[string]any{"choices": []any{map[string]any{
		"index": 0.0,
		"delta": map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"index": 0.0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "real", "arguments": "{}"},
			}},
		},
	}}}, state)
	events = append(events, ProcessChunk(orphanChunk("<｜DSML｜invoke name=\"evil\">\n<｜DSML｜parameter name=\"a\" string=\"true\">1</｜DSML｜parameter>\n</｜DSML｜invoke>"), state)...)
	events = append(events, ProcessChunk(orphanFinish("tool_calls"), state)...)
	joined := strings.Join(events, "\n")
	if strings.Contains(joined, `"name":"evil"`) {
		t.Errorf("orphan must not double-call beside native, got:\n%s", joined)
	}
	if !strings.Contains(joined, `"name":"real"`) {
		t.Errorf("expected native tool kept, got:\n%s", joined)
	}
}

func TestStream_OrphanInReasoningIsNotAToolCall(t *testing.T) {
	state := NewStreamState()
	events := ProcessChunk(map[string]any{"choices": []any{map[string]any{
		"index": 0.0,
		"delta": map[string]any{"reasoning_content": "Maybe <｜DSML｜invoke name=\"x\">\n<｜DSML｜parameter name=\"a\" string=\"true\">1</｜DSML｜parameter>\n</｜DSML｜invoke> is wrong"},
	}}}, state)
	events = append(events, ProcessChunk(orphanFinish("stop"), state)...)
	joined := strings.Join(events, "\n")
	if strings.Contains(joined, `"type":"tool_use"`) {
		t.Errorf("reasoning invoke must not become tool_use, got:\n%s", joined)
	}
	if !strings.Contains(joined, "thinking_delta") {
		t.Errorf("expected thinking events, got:\n%s", joined)
	}
}

func TestStream_PlainProseUnaffected(t *testing.T) {
	state := NewStreamState()
	events := ProcessChunk(orphanChunk("Hello world, no markers here."), state)
	joined := strings.Join(events, "\n")
	if !strings.Contains(joined, "Hello world") {
		t.Errorf("expected immediate prose flush, got:\n%s", joined)
	}
	if state.holdBuf.Len() != 0 {
		t.Errorf("expected empty hold buffer, got %q", state.holdBuf.String())
	}
}

func TestCouldBeInvokeOpener(t *testing.T) {
	hold := []string{"<", "<｜", "<｜DSML｜inv", "<invoke name=\"x", "<parameter", "<|dsml|invoke", "<｜dsml｜tool_calls>"}
	flush := []string{"</", "</tool_calls", "<div>", "<feature-flag>", "a < b", "</plan>", "<input type=\"text\">", "<parameterized>"}
	for _, s := range hold {
		if !couldBeInvokeOpener(s) {
			t.Errorf("expected hold for %q", s)
		}
	}
	for _, s := range flush {
		if couldBeInvokeOpener(s) {
			t.Errorf("expected flush for %q", s)
		}
	}
}

func TestEmitOrphanToolUse_ArgsValidJSON(t *testing.T) {
	state := NewStreamState()
	events := emitOrphanToolUse(OrphanToolCall{Name: "terminal", Input: `{"command":"echo hi"}`}, state)
	joined := strings.Join(events, "\n")
	if !strings.Contains(joined, `"type":"tool_use"`) {
		t.Fatalf("expected tool_use start, got:\n%s", joined)
	}
	events = append(events, handleFinish(state)...)
	joined = strings.Join(append([]string{joined}, events...), "\n")
	var found bool
	for _, line := range strings.Split(joined, "\n") {
		if strings.Contains(line, "input_json_delta") {
			var evt map[string]any
			start := strings.Index(line, "{")
			if start < 0 || json.Unmarshal([]byte(line[start:]), &evt) != nil {
				continue
			}
			delta, _ := evt["delta"].(map[string]any)
			pj, _ := delta["partial_json"].(string)
			var o map[string]any
			if err := json.Unmarshal([]byte(pj), &o); err != nil {
				t.Fatalf("input_json_delta not valid JSON object: %v", pj)
			}
			if o["command"] != "echo hi" {
				t.Fatalf("unexpected args: %v", o)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("expected input_json_delta with args, got:\n%s", joined)
	}
}

func TestStream_ParallelOrphansAcrossDeltas(t *testing.T) {
	d := "｜DSML｜"
	mk := func(name, arg string) string {
		return "<" + d + "invoke name=\"" + name + "\">\n<" + d + "parameter name=\"cmd\" string=\"true\">" + arg + "</" + d + "parameter>\n</" + d + "invoke>"
	}
	text := "go\n" + mk("a", "ls") + " mid " + mk("b", "pwd")
	state := NewStreamState()
	joined := feedText(t, state, text, 9, "stop")
	for _, want := range []string{`"name":"a"`, `"name":"b"`, `"type":"tool_use"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected %s in output, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, " mid ") {
		t.Errorf("expected inter-call prose dropped, got:\n%s", joined)
	}
}
