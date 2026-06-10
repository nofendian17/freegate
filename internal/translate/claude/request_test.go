package claude

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestClaudeToOpenAI_BasicText(t *testing.T) {
	claude := `{"model":"claude-sonnet-4","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	result, err := ToOpenAI([]byte(claude))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	if err := json.Unmarshal(result, &openai); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}

	if openai["model"] != "claude-sonnet-4" {
		t.Errorf("expected model=claude-sonnet-4, got %v", openai["model"])
	}
	if openai["max_tokens"] != float64(100) {
		t.Errorf("expected max_tokens=100, got %v", openai["max_tokens"])
	}

	msgs, ok := openai["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %v", msgs)
	}
	msg := msgs[0].(map[string]any)
	if msg["role"] != "user" {
		t.Errorf("expected role=user, got %v", msg["role"])
	}
	if msg["content"] != "hi" {
		t.Errorf("expected content=hi, got %v", msg["content"])
	}
}

func TestClaudeToOpenAI_SystemString(t *testing.T) {
	claude := `{"model":"claude","system":"You are helpful","messages":[{"role":"user","content":"hi"}]}`
	result, err := ToOpenAI([]byte(claude))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	msgs := openai["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system+user), got %d", len(msgs))
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" {
		t.Errorf("expected system role, got %v", sys["role"])
	}
	if sys["content"] != "You are helpful" {
		t.Errorf("expected system content, got %v", sys["content"])
	}
}

func TestClaudeToOpenAI_SystemArray(t *testing.T) {
	claude := `{"model":"claude","system":[{"type":"text","text":"You are"},{"type":"text","text":"helpful"}],"messages":[{"role":"user","content":"hi"}]}`
	result, err := ToOpenAI([]byte(claude))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	msgs := openai["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "You are\nhelpful" {
		t.Errorf("unexpected system content: %v", sys["content"])
	}
}

func TestClaudeToOpenAI_WithTools(t *testing.T) {
	body := `{
		"model":"claude","max_tokens":100,
		"messages":[{"role":"user","content":"weather"}],
		"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"tool_choice":{"type":"any"}
	}`
	result, err := ToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)

	tools, ok := openai["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %v", tools)
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Errorf("expected type=function, got %v", tool["type"])
	}
	fn := tool["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("expected name=get_weather, got %v", fn["name"])
	}

	tc := openai["tool_choice"]
	if tc != "required" {
		t.Errorf("expected tool_choice=required, got %v", tc)
	}
}

func TestClaudeToOpenAI_Image(t *testing.T) {
	body := `{
		"model":"claude","max_tokens":100,
		"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"/9j/4AAQ"}}]}]
	}`
	result, err := ToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	msgs := openai["messages"].([]any)
	msg := msgs[0].(map[string]any)
	content := msg["content"].([]any)
	img := content[0].(map[string]any)
	if img["type"] != "image_url" {
		t.Errorf("expected type=image_url, got %v", img["type"])
	}
	url, _ := img["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Errorf("expected data uri, got %s", url)
	}
}

func TestClaudeToOpenAI_ToolUseAndResult(t *testing.T) {
	body := `{
		"model":"claude","max_tokens":100,
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"get_weather","input":{"city":"NYC"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"sunny"}]}
		]
	}`
	result, err := ToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	msgs := openai["messages"].([]any)

	// First should be assistant with tool_calls
	asst := msgs[0].(map[string]any)
	if asst["role"] != "assistant" {
		t.Errorf("expected role=assistant, got %v", asst["role"])
	}
	tcs := asst["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(tcs))
	}
	tc := tcs[0].(map[string]any)
	if tc["id"] != "tu_1" {
		t.Errorf("expected id=tu_1, got %v", tc["id"])
	}

	// Second should be tool role
	tool := msgs[1].(map[string]any)
	if tool["role"] != "tool" {
		t.Errorf("expected role=tool, got %v", tool["role"])
	}
	if tool["tool_call_id"] != "tu_1" {
		t.Errorf("expected tool_call_id=tu_1, got %v", tool["tool_call_id"])
	}
}

func TestClaudeToOpenAI_UserMessageWithTextAndToolResult(t *testing.T) {
	body := `{
		"model":"claude","max_tokens":100,
		"messages":[
			{"role":"user","content":"What is the weather?"},
			{"role":"assistant","content":[
				{"type":"text","text":"Let me check."},
				{"type":"tool_use","id":"tu_1","name":"get_weather","input":{"city":"NYC"}}
			]},
			{"role":"user","content":[
				{"type":"text","text":"Note: please retry."},
				{"type":"tool_result","tool_use_id":"tu_1","content":"sunny, 72F"}
			]}
		]
	}`
	result, err := ToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	if err := json.Unmarshal(result, &openai); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}

	msgs, ok := openai["messages"].([]any)
	if !ok {
		t.Fatalf("expected messages array, got %T", openai["messages"])
	}

	for i, m := range msgs {
		if _, ok := m.(map[string]any); !ok {
			t.Errorf("message %d is %T, want map (nested array bug): %+v", i, m, msgs)
		}
	}

	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages (system+nothing+assistant+tool+user from split), got %d: %+v", len(msgs), msgs)
	}

	// OpenAI-idiomatic order: the tool response must come right after the
	// assistant tool_calls, before any user text from the same Claude
	// user message. Putting the user text first causes
	// FixMissingToolResponses to insert a synthetic duplicate tool
	// message (see TestRequest_ClaudeMixedTextAndToolResult_NoDuplicateToolCallID).
	tool, _ := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "tu_1" {
		t.Errorf("expected msg[2]={role:tool,tool_call_id:tu_1}, got %+v", tool)
	}
	textUser, _ := msgs[3].(map[string]any)
	if textUser["role"] != "user" || textUser["content"] != "Note: please retry." {
		t.Errorf("expected msg[3]={role:user,content:\"Note: please retry.\"}, got %+v", textUser)
	}
}

func TestClaudeToOpenAI_StopSequences(t *testing.T) {
	body := `{"model":"claude","stop_sequences":["\n\nHuman:","\n\nAssistant:"],"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	result, err := ToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	stop := openai["stop"].([]any)
	if len(stop) != 2 {
		t.Errorf("expected 2 stop sequences, got %d", len(stop))
	}
}

func TestClaudeToOpenAI_AssistantStringContent(t *testing.T) {
	body := `{"model":"claude","max_tokens":100,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`
	result, err := ToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	msgs := openai["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
}

func TestClaudeToOpenAI_StreamTrue(t *testing.T) {
	body := `{"model":"claude","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	result, err := ToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	if openai["stream"] != true {
		t.Errorf("expected stream=true, got %v", openai["stream"])
	}
}

func TestClaudeToOpenAI_EmptyBody(t *testing.T) {
	_, err := ToOpenAI([]byte{})
	if err == nil {
		t.Error("expected error for empty body")
	}
}

func TestClaudeToOpenAI_InvalidJSON(t *testing.T) {
	_, err := ToOpenAI([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// Helper: round-trip test for tool_choice auto
func TestClaudeToOpenAI_ToolChoiceAuto(t *testing.T) {
	body := `{"model":"claude","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"auto"}}`
	result, err := ToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	if openai["tool_choice"] != "auto" {
		t.Errorf("expected tool_choice=auto, got %v", openai["tool_choice"])
	}
}

func TestClaudeToOpenAI_ThinkingBlockSetsReasoningContent(t *testing.T) {
	body := `{
		"model":"deepseek-reasoner",
		"max_tokens":100,
		"messages":[
			{"role":"user","content":"Q1"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"Let me reason..."},
				{"type":"text","text":"The answer is 42."}
			]},
			{"role":"user","content":"Q2"}
		]
	}`
	result, err := ToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var openai map[string]any
	if err := json.Unmarshal(result, &openai); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	msgs := openai["messages"].([]any)
	// Find assistant message
	var asstMsg map[string]any
	for _, m := range msgs {
		msg := m.(map[string]any)
		if msg["role"] == "assistant" {
			asstMsg = msg
			break
		}
	}
	if asstMsg == nil {
		t.Fatal("assistant message not found")
	}
	rc, ok := asstMsg["reasoning_content"].(string)
	if !ok || rc != "Let me reason..." {
		t.Errorf("expected reasoning_content='Let me reason...', got %v", asstMsg["reasoning_content"])
	}
	// Text content should NOT contain the thinking text
	if strings.Contains(fmt.Sprintf("%v", asstMsg["content"]), "Let me reason") {
		t.Error("thinking text should not appear in content")
	}
}

// Benchmark the Claude→OpenAI request translation
func BenchmarkClaudeToOpenAI(b *testing.B) {
	body := []byte(`{
		"model":"claude-sonnet-4-20250514",
		"max_tokens":1000,
		"system":"You are a helpful assistant.",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"Hello"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"/9j/4AAQSkZJRg=="}}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"get_weather","input":{"city":"NYC"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"Sunny"}]}
		],
		"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ToOpenAI(body)
	}
}
