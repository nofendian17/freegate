package claude

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const orphanSigil = "｜DSML｜"

func orphanBlock(name, params string) string {
	return "<" + orphanSigil + "invoke name=\"" + name + "\">\n" + params + "\n</" + orphanSigil + "invoke>"
}

func orphanParam(name, attr, value string) string {
	return "<" + orphanSigil + "parameter name=\"" + name + "\" string=\"" + attr + "\">" + value + "</" + orphanSigil + "parameter>"
}

func decodeArgs(t *testing.T, input string) map[string]any {
	t.Helper()
	var o map[string]any
	if err := json.Unmarshal([]byte(input), &o); err != nil {
		t.Fatalf("args are not a JSON object: %v (input=%s)", err, input)
	}
	return o
}

func TestExtractOrphanInvokes_Basic(t *testing.T) {
	text := "Let me run it.\n" + orphanBlock("terminal", orphanParam("command", "true", "echo hi")) + "\n"
	prose, calls, rest := ExtractOrphanInvokes(text)
	if prose != "Let me run it.\n" {
		t.Errorf("expected leading prose kept, got %q", prose)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != "terminal" {
		t.Errorf("expected name terminal, got %q", calls[0].Name)
	}
	if got := decodeArgs(t, calls[0].Input); !reflect.DeepEqual(got, map[string]any{"command": "echo hi"}) {
		t.Errorf("unexpected args: %v", got)
	}
	if rest != "\n" {
		t.Errorf("expected trailing newline as rest, got %q", rest)
	}
}

func TestExtractOrphanInvokes_TrailingEndWrapper(t *testing.T) {
	text := "Let me run it.\n" + orphanBlock("terminal", orphanParam("command", "true", "echo hi")) + "<" + orphanSigil + "tool_calls>"
	prose, calls, rest := ExtractOrphanInvokes(text)
	if len(calls) != 1 || calls[0].Name != "terminal" {
		t.Fatalf("expected 1 terminal call, got %+v", calls)
	}
	if prose != "Let me run it.\n" {
		t.Errorf("expected leading prose kept, got %q", prose)
	}
	_ = rest // caller drops trailing (tool call ends the turn)
}

func TestExtractOrphanInvokes_Parallel(t *testing.T) {
	text := orphanBlock("a", orphanParam("x", "true", "1")) + orphanBlock("b", orphanParam("y", "true", "2"))
	_, calls, _ := ExtractOrphanInvokes(text)
	if len(calls) != 2 || calls[0].Name != "a" || calls[1].Name != "b" {
		t.Fatalf("expected parallel a,b calls, got %+v", calls)
	}
}

func TestExtractOrphanInvokes_IncompleteNotRecovered(t *testing.T) {
	text := "Sure.\n<" + orphanSigil + "invoke name=\"terminal\">\n" + orphanParam("command", "true", "echo hi")
	prose, calls, rest := ExtractOrphanInvokes(text)
	if len(calls) != 0 {
		t.Fatalf("expected no calls for incomplete block, got %+v", calls)
	}
	if rest != text {
		t.Errorf("expected full text retained, got %q", rest)
	}
	if prose != "" {
		t.Errorf("expected empty prose, got %q", prose)
	}
}

func TestExtractOrphanInvokes_ProseMentioningMarker(t *testing.T) {
	text := "The <invoke> tag starts tool calls in DSML prose."
	_, calls, rest := ExtractOrphanInvokes(text)
	if len(calls) != 0 {
		t.Fatalf("expected no calls for mere mention, got %+v", calls)
	}
	if rest != text {
		t.Errorf("expected text retained, got %q", rest)
	}
}

func TestExtractOrphanInvokes_BareForm(t *testing.T) {
	text := `<invoke name="search"><parameter name="query">vllm</parameter></invoke>`
	_, calls, _ := ExtractOrphanInvokes(text)
	if len(calls) != 1 || calls[0].Name != "search" {
		t.Fatalf("expected 1 search call, got %+v", calls)
	}
	if got := decodeArgs(t, calls[0].Input); !reflect.DeepEqual(got, map[string]any{"query": "vllm"}) {
		t.Errorf("unexpected args: %v", got)
	}
}

func TestExtractOrphanInvokes_StringAttrConversion(t *testing.T) {
	inner := orphanParam("n", "false", "42") + orphanParam("s", "true", "42") +
		orphanParam("b", "false", "true") + orphanParam("u", "false", "Beijing")
	_, calls, _ := ExtractOrphanInvokes(orphanBlock("score", inner))
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %+v", calls)
	}
	got := decodeArgs(t, calls[0].Input)
	want := map[string]any{"n": float64(42), "s": "42", "b": true, "u": "Beijing"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExtractOrphanInvokes_WrapperUnwrap(t *testing.T) {
	for _, wrapper := range []string{"arguments", "input"} {
		inner := "<" + orphanSigil + "parameter name=\"" + wrapper + "\" string=\"false\">{\"location\":\"Beijing\"}</" + orphanSigil + "parameter>"
		_, calls, _ := ExtractOrphanInvokes(orphanBlock("get_weather", inner))
		if len(calls) != 1 {
			t.Fatalf("%s: expected 1 call, got %+v", wrapper, calls)
		}
		if got := decodeArgs(t, calls[0].Input); !reflect.DeepEqual(got, map[string]any{"location": "Beijing"}) {
			t.Errorf("%s: expected unwrapped args, got %v", wrapper, got)
		}
	}
}

func TestExtractOrphanInvokes_Empty(t *testing.T) {
	if _, calls, rest := ExtractOrphanInvokes(""); len(calls) != 0 || rest != "" {
		t.Errorf("expected empty result, got %+v %q", calls, rest)
	}
	if _, calls, rest := ExtractOrphanInvokes("plain prose, no markers"); len(calls) != 0 || rest != "plain prose, no markers" {
		t.Errorf("expected passthrough, got %+v %q", calls, rest)
	}
}

func TestHasInvokeOpener(t *testing.T) {
	for _, s := range []string{"<" + orphanSigil + "invoke name=\"x", "</parameter", "<invoke", "text <｜DSML｜tool_calls>"} {
		if !HasInvokeOpener(s) {
			t.Errorf("expected opener detected in %q", s)
		}
	}
	for _, s := range []string{"plain prose", "a < b comparison", "done."} {
		if HasInvokeOpener(s) {
			t.Errorf("expected no opener in %q", s)
		}
	}
}

func TestExtractOrphanInvokes_NoDSMLLeakInOutput(t *testing.T) {
	text := "pre\n" + orphanBlock("terminal", orphanParam("command", "true", "echo hi")) + "Done."
	prose, calls, _ := ExtractOrphanInvokes(text)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %+v", calls)
	}
	for _, s := range []string{prose, calls[0].Name, calls[0].Input} {
		if strings.Contains(s, "DSML") {
			t.Errorf("DSML leaked into output: %q", s)
		}
	}
}
