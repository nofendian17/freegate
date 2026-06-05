package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFromOpenAI_BasicText(t *testing.T) {
	in := `{"model":"x","messages":[{"role":"user","content":"hi"}]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["model"] != "x" {
		t.Errorf("model=%v want x", got["model"])
	}
	if _, ok := got["system"]; ok {
		t.Errorf("expected no system field, got %v", got["system"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages=%v", msgs)
	}
	m0, _ := msgs[0].(map[string]any)
	if m0["role"] != "user" {
		t.Errorf("role=%v want user", m0["role"])
	}
	content, _ := m0["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content=%v", content)
	}
	b0, _ := content[0].(map[string]any)
	if b0["type"] != "text" || b0["text"] != "hi" {
		t.Errorf("block=%+v want text/hi", b0)
	}
}

func TestFromOpenAI_SystemMessageMerge(t *testing.T) {
	in := `{"messages":[
		{"role":"system","content":"be helpful"},
		{"role":"system","content":"be concise"},
		{"role":"user","content":"hi"}
	]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	sys, _ := got["system"].([]any)
	if len(sys) != 2 {
		t.Fatalf("expected 2 system blocks, got %d", len(sys))
	}
	if sys[0].(map[string]any)["text"] != "be helpful" {
		t.Errorf("system[0] text=%v want be helpful", sys[0].(map[string]any)["text"])
	}
	if sys[1].(map[string]any)["text"] != "be concise" {
		t.Errorf("system[1] text=%v want be concise", sys[1].(map[string]any)["text"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Errorf("system messages should be filtered out, got %d user messages", len(msgs))
	}
}

func TestFromOpenAI_ToolCall(t *testing.T) {
	in := `{"messages":[
		{"role":"user","content":"what's the weather?"},
		{"role":"assistant","content":"","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}
		]},
		{"role":"tool","tool_call_id":"call_1","content":"72F"}
	]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d (%+v)", len(got.Messages), got.Messages)
	}
	// Assistant message should contain a tool_use block.
	a := got.Messages[1]
	if a["role"] != "assistant" {
		t.Errorf("msg[1] role=%v want assistant", a["role"])
	}
	content, _ := a["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["type"] != "tool_use" {
		t.Errorf("assistant should have tool_use block, got %+v", content)
	}
	// Tool message should become a user message with tool_result.
	tr := got.Messages[2]
	if tr["role"] != "user" {
		t.Errorf("msg[2] role=%v want user", tr["role"])
	}
	tcontent, _ := tr["content"].([]any)
	if len(tcontent) != 1 || tcontent[0].(map[string]any)["type"] != "tool_result" {
		t.Errorf("expected single tool_result block, got %+v", tcontent)
	}
}

func TestFromOpenAI_ImageDataURL(t *testing.T) {
	in := `{"messages":[
		{"role":"user","content":[
			{"type":"text","text":"what is in this image?"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}
		]}
	]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got.Messages))
	}
	blocks := got.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0]["type"] != "text" {
		t.Errorf("blocks[0] type=%v want text", blocks[0]["type"])
	}
	if blocks[1]["type"] != "image" {
		t.Errorf("blocks[1] type=%v want image", blocks[1]["type"])
	}
	src, _ := blocks[1]["source"].(map[string]any)
	if src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != "QUJD" {
		t.Errorf("image source=%+v", src)
	}
}

func TestFromOpenAI_ImageHTTPURL(t *testing.T) {
	in := `{"messages":[
		{"role":"user","content":[
			{"type":"image_url","image_url":{"url":"https://example.com/x.png"}}
		]}
	]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(out, &got)
	src, _ := got.Messages[0].Content[0]["source"].(map[string]any)
	if src["type"] != "url" || src["url"] != "https://example.com/x.png" {
		t.Errorf("image url source=%+v", src)
	}
}

func TestFromOpenAI_Tools(t *testing.T) {
	in := `{"tools":[
		{"type":"function","function":{"name":"f","description":"a tool","parameters":{"type":"object","properties":{"x":{"type":"string"}}}}}
	]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Tools []map[string]any `json:"tools"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(got.Tools))
	}
	t0 := got.Tools[0]
	if t0["name"] != "f" || t0["description"] != "a tool" {
		t.Errorf("tool=%+v", t0)
	}
	schema, ok := t0["input_schema"].(map[string]any)
	if !ok {
		t.Fatalf("expected input_schema, got %+v", t0)
	}
	if schema["type"] != "object" {
		t.Errorf("schema.type=%v want object", schema["type"])
	}
}

func TestFromOpenAI_ToolChoice(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantTyp string
		wantNm  string // if type=tool
	}{
		{"auto string", `{"tool_choice":"auto"}`, "auto", ""},
		{"required string", `{"tool_choice":"required"}`, "any", ""},
		{"none string", `{"tool_choice":"none"}`, "auto", ""},
		{"function object", `{"tool_choice":{"type":"function","function":{"name":"f"}}}`, "tool", "f"},
		{"claude-style tool", `{"tool_choice":{"type":"tool","name":"f"}}`, "tool", "f"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := FromOpenAI([]byte(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got struct {
				ToolChoice map[string]any `json:"tool_choice"`
			}
			_ = json.Unmarshal(out, &got)
			if got.ToolChoice["type"] != tc.wantTyp {
				t.Errorf("type=%v want %v", got.ToolChoice["type"], tc.wantTyp)
			}
			if tc.wantNm != "" {
				if got.ToolChoice["name"] != tc.wantNm {
					t.Errorf("name=%v want %v", got.ToolChoice["name"], tc.wantNm)
				}
			}
		})
	}
}

func TestFromOpenAI_ReasoningEffort(t *testing.T) {
	tests := []struct {
		effort      string
		wantBudget  int
		wantPresent bool
	}{
		{"none", 0, false},
		{"low", 4096, true},
		{"medium", 8192, true},
		{"high", 16384, true},
		{"xhigh", 32768, true},
		{"unknown", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.effort, func(t *testing.T) {
			body := `{"reasoning_effort":"` + tc.effort + `"}`
			out, err := FromOpenAI([]byte(body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got map[string]any
			_ = json.Unmarshal(out, &got)
			th, present := got["thinking"].(map[string]any)
			if present != tc.wantPresent {
				t.Fatalf("thinking present=%v want %v (output=%s)", present, tc.wantPresent, out)
			}
			if present {
				if th["type"] != "enabled" {
					t.Errorf("thinking.type=%v want enabled", th["type"])
				}
				if th["budget_tokens"].(float64) != float64(tc.wantBudget) {
					t.Errorf("thinking.budget_tokens=%v want %v", th["budget_tokens"], tc.wantBudget)
				}
			}
		})
	}
}

func TestFromOpenAI_ResponseFormat(t *testing.T) {
	in := `{
		"response_format":{"type":"json_object"},
		"messages":[{"role":"user","content":"hi"}]
	}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		System []map[string]any `json:"system"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.System) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(got.System))
	}
	if !strings.Contains(got.System[0]["text"].(string), "valid JSON") {
		t.Errorf("system text=%q", got.System[0]["text"])
	}
}

func TestFromOpenAI_MessagesConsecutiveUserMerged(t *testing.T) {
	in := `{"messages":[
		{"role":"user","content":"hello"},
		{"role":"user","content":"world"}
	]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.Messages) != 1 {
		t.Fatalf("expected 1 merged user message, got %d", len(got.Messages))
	}
	if len(got.Messages[0].Content) != 2 {
		t.Errorf("expected 2 text blocks, got %d", len(got.Messages[0].Content))
	}
}

func TestFromOpenAI_ToolUseFlushedToOwnMessage(t *testing.T) {
	// An assistant tool_use must end the assistant message so the
	// following tool result is in its own user message.
	in := `{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"calling tool","tool_calls":[
			{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}
		]},
		{"role":"user","content":"and another question"}
	]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got.Messages))
	}
	// After tool_use, the user message should not be merged with the
	// assistant message.
	if got.Messages[1].Role != "assistant" {
		t.Errorf("msg[1] role=%v", got.Messages[1].Role)
	}
	if got.Messages[2].Role != "user" {
		t.Errorf("msg[2] role=%v", got.Messages[2].Role)
	}
}

func TestFromOpenAI_MultipleToolResultsGrouped(t *testing.T) {
	// An assistant turn with N tool_calls followed by N tool messages
	// must collapse into a single user message with N tool_result
	// blocks — Claude rejects the alternative shape (consecutive user
	// turns, or tool_results orphaned from their tool_use).
	in := `{"messages":[
		{"role":"user","content":"what's the weather in SF and NYC?"},
		{"role":"assistant","content":"","tool_calls":[
			{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}},
			{"id":"c2","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}
		]},
		{"role":"tool","tool_call_id":"c1","content":"72F"},
		{"role":"tool","tool_call_id":"c2","content":"55F"}
	]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// Expect: [user, assistant{tool_use c1, tool_use c2}, user{tool_result c1, tool_result c2}]
	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages (grouped), got %d: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[2].Role != "user" {
		t.Errorf("msg[2] role=%q want user", got.Messages[2].Role)
	}
	results := got.Messages[2].Content
	if len(results) != 2 {
		t.Fatalf("expected 2 tool_result blocks in msg[2], got %d: %+v", len(results), results)
	}
	gotIDs := map[string]bool{}
	for _, b := range results {
		if b["type"] != "tool_result" {
			t.Errorf("msg[2] block type=%q want tool_result", b["type"])
		}
		id, _ := b["tool_use_id"].(string)
		gotIDs[id] = true
	}
	for _, want := range []string{"c1", "c2"} {
		if !gotIDs[want] {
			t.Errorf("missing tool_result for %q in msg[2]; got ids=%v", want, gotIDs)
		}
	}
}

func TestFromOpenAI_ResponseFormatSchemaCompact(t *testing.T) {
	// The JSON schema inside the system block is wrapped in a markdown
	// fence for Claude to read. It does not need to be indented —
	// indentation is wasted CPU on the encoder and wasted bytes for
	// every request with response_format=json_schema.
	in := `{
		"response_format":{
			"type":"json_schema",
			"json_schema":{"schema":{"type":"object","properties":{"a":{"type":"string"}}}}
		},
		"messages":[{"role":"user","content":"hi"}]
	}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got.System) == 0 {
		t.Fatal("expected a system block")
	}
	text := got.System[0].Text
	// Indented JSON has lines that start with newline + 2 spaces.
	// Compact JSON (json.Marshal) has neither.
	if strings.Contains(text, "\n  ") {
		t.Errorf("expected compact JSON, but output contains indented lines:\n%s", text)
	}
}

func TestFromOpenAI_EmptyBody(t *testing.T) {
	_, err := FromOpenAI([]byte(`{`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestFromOpenAI_StopToStopSequences(t *testing.T) {
	// OpenAI's `stop` field (string or array) maps to Claude's
	// `stop_sequences` (always an array). A string is wrapped into a
	// single-element array; an array passes through.
	tests := []struct {
		name string
		in   string
		want []any
	}{
		{"string", `{"stop":"\n"}`, []any{"\n"}},
		{"array", `{"stop":["END","STOP"]}`, []any{"END", "STOP"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := FromOpenAI([]byte(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			seqs, ok := got["stop_sequences"].([]any)
			if !ok {
				t.Fatalf("stop_sequences=%v (type %T) want []any", got["stop_sequences"], got["stop_sequences"])
			}
			if len(seqs) != len(tc.want) {
				t.Fatalf("stop_sequences=%v want %v", seqs, tc.want)
			}
			for i, want := range tc.want {
				if seqs[i] != want {
					t.Errorf("stop_sequences[%d]=%v want %v", i, seqs[i], want)
				}
			}
		})
	}
}

func TestFromOpenAI_UnknownRoleRejected(t *testing.T) {
	// Unknown roles (e.g. typos, future OpenAI roles we don't
	// support) must surface as a translation error, not be silently
	// coerced into a text block — silent coercion masks upstream
	// schema errors and produces invalid Claude requests.
	in := `{"messages":[
		{"role":"user","content":"hi"},
		{"role":"asistant","content":"oops typo"}
	]}`
	_, err := FromOpenAI([]byte(in))
	if err == nil {
		t.Fatal("expected error for unknown role, got nil")
	}
}

func TestFromOpenAI_ToolArgsInvalidJSON(t *testing.T) {
	// A tool_call with malformed `arguments` JSON must surface an error
	// instead of silently invoking the tool with input={}. A model that
	// emits invalid args would otherwise be called server-side with
	// the wrong (empty) payload — for tools that require non-optional
	// fields, this turns a model bug into a destructive operation.
	in := `{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[
			{"id":"c1","type":"function","function":{"name":"delete_file","arguments":"{not valid json"}}
		]}
	]}`
	_, err := FromOpenAI([]byte(in))
	if err == nil {
		t.Fatal("expected error for invalid tool arguments JSON, got nil")
	}
}

func TestFromOpenAI_MaxCompletionTokens(t *testing.T) {
	// OpenAI's `max_completion_tokens` (the newer o1-era field) maps
	// to Claude's `max_tokens`. When both are set, the newer field
	// wins, matching OpenAI API behavior.
	tests := []struct {
		name string
		in   string
		want float64
	}{
		{"only max_completion_tokens", `{"max_completion_tokens":256}`, 256},
		{"max_completion_tokens overrides max_tokens", `{"max_tokens":100,"max_completion_tokens":256}`, 256},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := FromOpenAI([]byte(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if got["max_tokens"] != tc.want {
				t.Errorf("max_tokens=%v want %v", got["max_tokens"], tc.want)
			}
		})
	}
}
