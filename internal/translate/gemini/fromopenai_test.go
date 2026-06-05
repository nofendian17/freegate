package gemini

import (
	"encoding/json"
	"testing"
)

func TestFromOpenAI_BasicText(t *testing.T) {
	in := `{"model":"gemini-x","messages":[{"role":"user","content":"hi"}]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["model"] != "gemini-x" {
		t.Errorf("model=%v want gemini-x", got["model"])
	}
	contents, _ := got["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(contents))
	}
	c0, _ := contents[0].(map[string]any)
	if c0["role"] != "user" {
		t.Errorf("role=%v want user", c0["role"])
	}
	parts, _ := c0["parts"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "hi" {
		t.Errorf("parts=%+v want [text/hi]", parts)
	}
}

func TestFromOpenAI_SystemMessage(t *testing.T) {
	in := `{"messages":[
		{"role":"system","content":"be helpful"},
		{"role":"user","content":"hi"}
	]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	sys, ok := got["systemInstruction"].(map[string]any)
	if !ok {
		t.Fatalf("systemInstruction missing: %+v", got)
	}
	parts, _ := sys["parts"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "be helpful" {
		t.Errorf("system parts=%+v", parts)
	}
	// System message should NOT also appear in contents.
	contents, _ := got["contents"].([]any)
	for _, cAny := range contents {
		c, _ := cAny.(map[string]any)
		if r, _ := c["role"].(string); r == "system" {
			t.Errorf("system role should not appear in contents")
		}
	}
}

func TestFromOpenAI_ImageDataURL(t *testing.T) {
	in := `{"messages":[
		{"role":"user","content":[
			{"type":"text","text":"what is this?"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}
		]}
	]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	contents, _ := got["contents"].([]any)
	parts, _ := contents[0].(map[string]any)["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	if parts[1].(map[string]any)["inlineData"] == nil {
		t.Errorf("second part should be inlineData, got %+v", parts[1])
	}
}

func TestFromOpenAI_ToolCall(t *testing.T) {
	in := `{"messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":"","tool_calls":[
			{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}
		]},
		{"role":"tool","tool_call_id":"c1","content":"72F"}
	]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	contents, _ := got["contents"].([]any)
	// Expected: [user, model+functionCall, user+functionResponse]
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents, got %d (%+v)", len(contents), contents)
	}
	if contents[1].(map[string]any)["role"] != "model" {
		t.Errorf("contents[1] role=%v want model", contents[1].(map[string]any)["role"])
	}
	modelParts, _ := contents[1].(map[string]any)["parts"].([]any)
	if modelParts[0].(map[string]any)["functionCall"] == nil {
		t.Errorf("model parts[0] should be functionCall, got %+v", modelParts[0])
	}
	// Third content: user with functionResponse
	if contents[2].(map[string]any)["role"] != "user" {
		t.Errorf("contents[2] role=%v want user", contents[2].(map[string]any)["role"])
	}
	respParts, _ := contents[2].(map[string]any)["parts"].([]any)
	if respParts[0].(map[string]any)["functionResponse"] == nil {
		t.Errorf("contents[2] parts[0] should be functionResponse, got %+v", respParts[0])
	}
}

func TestFromOpenAI_Tools(t *testing.T) {
	in := `{"tools":[
		{"type":"function","function":{"name":"f","description":"a tool","parameters":{"type":"object"}}}
	]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	t0 := tools[0].(map[string]any)
	decls, _ := t0["functionDeclarations"].([]any)
	if len(decls) != 1 {
		t.Fatalf("expected 1 functionDeclaration, got %d", len(decls))
	}
	d0, _ := decls[0].(map[string]any)
	if d0["name"] != "f" || d0["description"] != "a tool" {
		t.Errorf("declaration=%+v", d0)
	}
}

func TestFromOpenAI_GenerationConfig(t *testing.T) {
	in := `{
		"temperature": 0.7,
		"top_p": 0.9,
		"max_tokens": 100,
		"stop": ["\n\n"]
	}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	gc, _ := got["generationConfig"].(map[string]any)
	if gc["temperature"].(float64) != 0.7 {
		t.Errorf("temperature=%v", gc["temperature"])
	}
	if gc["topP"].(float64) != 0.9 {
		t.Errorf("topP=%v", gc["topP"])
	}
	if gc["maxOutputTokens"].(float64) != 100 {
		t.Errorf("maxOutputTokens=%v", gc["maxOutputTokens"])
	}
}

func TestFromOpenAI_ReasoningEffort(t *testing.T) {
	in := `{"reasoning_effort":"high"}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	gc, _ := got["generationConfig"].(map[string]any)
	tc, ok := gc["thinkingConfig"].(map[string]any)
	if !ok {
		t.Fatalf("thinkingConfig missing: %+v", gc)
	}
	if tc["thinkingBudget"].(float64) != 32768 {
		t.Errorf("thinkingBudget=%v want 32768", tc["thinkingBudget"])
	}
}

func TestFromOpenAI_StreamPassthrough(t *testing.T) {
	in := `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	out, err := FromOpenAI([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["stream"] != true {
		t.Errorf("stream=%v want true", got["stream"])
	}
}
