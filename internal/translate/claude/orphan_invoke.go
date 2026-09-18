package claude

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Orphan-invoke recovery (vllm#48931 / vllm#55954 port).
//
// At long context DeepSeek-V4-Flash intermittently omits the
// `<｜DSML｜tool_calls>` START wrapper while still emitting a complete
// `<｜DSML｜invoke …>…</｜DSML｜invoke>` block. The upstream parser then
// returns the block as plain assistant content and no tool_calls are
// produced, so the client never executes the call.
//
// Freegate mirrors the official fix on the response path: the invoke
// prefix itself anchors tool-call detection, so a *complete* invoke block
// found in assistant content (with no native tool_calls present) is
// recovered into a real tool call instead of leaking as text (or being
// stripped entirely by SanitizeAssistantText).
//
// Guard rails (same as the official fix):
//   - Only *complete* blocks (open + matching close) are recovered; prose
//     merely mentioning a marker is never misparsed.
//   - A tool call ends the turn: text after the first invoke block is
//     dropped, never forwarded.
//   - Orphan invokes inside *reasoning* are not tool calls — callers must
//     only scan assistant content/reasoning_content text, never thinking.
//   - Native tool_calls always win: callers must skip recovery when the
//     message already carries tool calls.

// OrphanToolCall is a tool call recovered from a DSML invoke block.
type OrphanToolCall struct {
	// Name is the invoke name (the tool to call).
	Name string
	// Input is the JSON-encoded arguments object, repaired.
	Input string
}

var (
	// Invoke open/close, with optional ASCII/fullwidth/bare DSML sigils.
	// The name attribute is required: without it there is nothing to call.
	orphanInvokeOpenRe  = regexp.MustCompile(`(?is)<\s*(?:[|｜]DSML[|｜]\s*)?invoke\s+name="([^"]+)"\s*>`)
	orphanInvokeCloseRe = regexp.MustCompile(`(?i)</\s*(?:[|｜]DSML[|｜]\s*)?invoke\s*>`)
	// Parameter with optional string="true|false" attribute (vllm#41801
	// semantics: true keeps the literal, false converts via JSON).
	orphanParamRe = regexp.MustCompile(`(?is)<\s*(?:[|｜]DSML[|｜]\s*)?parameter\s+name="([^"]+)"(?:\s+string="(true|false)")?\s*>(.*?)</\s*(?:[|｜]DSML[|｜]\s*)?parameter\s*>`)
	// Lenient opener probe for end-of-stream triage (partial blocks).
	orphanInvokeOpenerRe = regexp.MustCompile(`(?i)<\s*/?\s*(?:[|｜]\s*DSML\s*[|｜]\s*)?(invoke|parameter|tool_calls|tool-calls)`)
)

// ExtractOrphanInvokes scans text for complete DSML invoke blocks.
//
// It returns the prose before the first block, the recovered calls in
// order, and the unconsumed remainder (text after the last complete
// block's close). Callers decide the remainder's fate: non-streaming
// drops it (a tool call ends the turn, per the official fix), streaming
// retains it until the stream ends in case a partial block completes.
//
// When no complete block exists, calls is nil and rest is the full text.
func ExtractOrphanInvokes(text string) (prose string, calls []OrphanToolCall, rest string) {
	if text == "" {
		return "", nil, ""
	}
	// Fast path: no invoke marker at all (case-insensitive scan without
	// allocating a lowered copy unless '<' is present).
	if !strings.Contains(text, "invoke") && !strings.Contains(text, "INVOKE") &&
		!strings.Contains(text, "Invoke") {
		return "", nil, text
	}
	var out []OrphanToolCall
	consumedThrough := -1
	searchFrom := 0
	for {
		loc := orphanInvokeOpenRe.FindStringSubmatchIndex(text[searchFrom:])
		if loc == nil {
			break
		}
		openStart, openEnd := searchFrom+loc[0], searchFrom+loc[1]
		name := text[searchFrom+loc[2] : searchFrom+loc[3]]
		closeLoc := orphanInvokeCloseRe.FindStringIndex(text[openEnd:])
		if closeLoc == nil {
			// Incomplete block: stop, leaving it (and everything after)
			// unconsumed for the caller to retain or flush as text.
			break
		}
		closeEnd := openEnd + closeLoc[1]
		inner := text[openEnd : openEnd+closeLoc[0]]
		if prose == "" && consumedThrough < 0 {
			prose = text[:openStart]
		}
		out = append(out, OrphanToolCall{Name: name, Input: orphanParamsToJSON(inner)})
		consumedThrough = closeEnd
		searchFrom = closeEnd
	}
	if len(out) == 0 {
		return "", nil, text
	}
	return prose, out, text[consumedThrough:]
}

// HasInvokeOpener reports whether s contains anything shaped like a DSML
// invoke/parameter/tool_calls opener. Used at end-of-stream to decide
// whether a trailing remainder is an in-progress block (forward as text,
// today's behavior) or plain trailing prose after a tool call (drop, per
// the official fix).
func HasInvokeOpener(s string) bool {
	return orphanInvokeOpenerRe.MatchString(s)
}

// orphanParamsToJSON converts an invoke block's inner parameter list to a
// JSON arguments object, following vllm#41801 semantics:
//
//   - string="true" keeps the value literally as a string;
//   - string="false" (or convertible unmarked values) converts JSON
//     scalars (42 → 42, true → true) and keeps the rest as strings;
//     unmarked values stay strings (conservative: freegate has no tool
//     schema to guide conversion);
//   - a single "arguments"/"input" wrapper whose inner value is a JSON
//     object is unwrapped (heuristic: without a schema we cannot verify
//     the wrapper is foreign, but a lone object-valued wrapper matches
//     the observed degenerate shape).
//
// The result always parses as a JSON object.
func orphanParamsToJSON(inner string) string {
	params := make(map[string]any)
	for _, m := range orphanParamRe.FindAllStringSubmatch(inner, -1) {
		name, attr, raw := m[1], m[2], m[3]
		params[name] = orphanConvertValue(raw, attr)
	}
	if len(params) == 1 {
		params = unwrapSingleWrapper(params)
	}
	if len(params) == 0 {
		return "{}"
	}
	b, err := json.Marshal(params)
	if err != nil {
		return "{}"
	}
	return repairToolArgs(string(b))
}

func orphanConvertValue(raw, attr string) any {
	if attr == "true" {
		return raw
	}
	if attr == "false" {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return raw
		}
		// A quoted value unmarshals back to string: keep the raw text
		// (identical) rather than re-encoding.
		if _, ok := v.(string); ok {
			return raw
		}
		return v
	}
	return raw
}

func unwrapSingleWrapper(params map[string]any) map[string]any {
	for _, wrapper := range []string{"arguments", "input"} {
		inner, ok := params[wrapper]
		if !ok {
			continue
		}
		switch v := inner.(type) {
		case map[string]any:
			return v
		case string:
			var o map[string]any
			if err := json.Unmarshal([]byte(v), &o); err == nil {
				return o
			}
		}
	}
	return params
}
