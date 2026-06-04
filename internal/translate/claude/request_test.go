package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClaudeToOpenAI_BasicText(t *testing.T) {
	claude := `{"model":"claude-sonnet-4","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	result, err := claudeToOpenAI([]byte(claude))
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
	result, err := claudeToOpenAI([]byte(claude))
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
	result, err := claudeToOpenAI([]byte(claude))
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
	result, err := claudeToOpenAI([]byte(body))
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
	result, err := claudeToOpenAI([]byte(body))
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
	result, err := claudeToOpenAI([]byte(body))
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

func TestClaudeToOpenAI_StopSequences(t *testing.T) {
	body := `{"model":"claude","stop_sequences":["\n\nHuman:","\n\nAssistant:"],"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	result, err := claudeToOpenAI([]byte(body))
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
	result, err := claudeToOpenAI([]byte(body))
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
	result, err := claudeToOpenAI([]byte(body))
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
	_, err := claudeToOpenAI([]byte{})
	if err == nil {
		t.Error("expected error for empty body")
	}
}

func TestClaudeToOpenAI_InvalidJSON(t *testing.T) {
	_, err := claudeToOpenAI([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// Helper: round-trip test for tool_choice auto
func TestClaudeToOpenAI_ToolChoiceAuto(t *testing.T) {
	body := `{"model":"claude","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"auto"}}`
	result, err := claudeToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	if openai["tool_choice"] != "auto" {
		t.Errorf("expected tool_choice=auto, got %v", openai["tool_choice"])
	}
}
