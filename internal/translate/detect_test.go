package translate

import (
	"testing"
)

func TestDetect_OpenAI(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if f := Detect(body); f != FormatOpenAI {
		t.Errorf("expected openai, got %s", f)
	}
}

func TestDetect_OpenAI_Multimodal(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]}]}`)
	if f := Detect(body); f != FormatOpenAI {
		t.Errorf("expected openai (image_url), got %s", f)
	}
}

func TestDetect_Claude_Basic(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	if f := Detect(body); f != FormatClaude {
		t.Errorf("expected claude (max_tokens), got %s", f)
	}
}

func TestDetect_Claude_SystemString(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","system":"You are helpful","messages":[{"role":"user","content":"hi"}]}`)
	if f := Detect(body); f != FormatClaude {
		t.Errorf("expected claude (system string), got %s", f)
	}
}

func TestDetect_Claude_SystemArray(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","system":[{"type":"text","text":"You are helpful"}],"messages":[{"role":"user","content":"hi"}]}`)
	if f := Detect(body); f != FormatClaude {
		t.Errorf("expected claude (system array), got %s", f)
	}
}

func TestDetect_Claude_Image(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","max_tokens":100,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"/9j/4AAQ"}}]}]}`)
	if f := Detect(body); f != FormatClaude {
		t.Errorf("expected claude (image base64), got %s", f)
	}
}

func TestDetect_Claude_ToolUse(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","max_tokens":100,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"get_weather","input":{"city":"NYC"}}]}]}`)
	if f := Detect(body); f != FormatClaude {
		t.Errorf("expected claude (tool_use), got %s", f)
	}
}

func TestDetect_Claude_ToolResult(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","max_tokens":100,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"sunny"}]}]}`)
	if f := Detect(body); f != FormatClaude {
		t.Errorf("expected claude (tool_result), got %s", f)
	}
}

func TestDetect_Claude_AnthropicVersion(t *testing.T) {
	body := []byte(`{"anthropic_version":"bedrock-2023-05-31","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	if f := Detect(body); f != FormatClaude {
		t.Errorf("expected claude (anthropic_version), got %s", f)
	}
}

func TestDetect_Gemini_Basic(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"hi"}],"role":"user"}]}`)
	if f := Detect(body); f != FormatGemini {
		t.Errorf("expected gemini, got %s", f)
	}
}

func TestDetect_Gemini_WithConfig(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"hello"}],"role":"user"}],"generationConfig":{"temperature":0.7}}`)
	if f := Detect(body); f != FormatGemini {
		t.Errorf("expected gemini, got %s", f)
	}
}

func TestDetect_EmptyBody(t *testing.T) {
	if f := Detect(nil); f != FormatOpenAI {
		t.Errorf("expected openai for nil, got %s", f)
	}
	if f := Detect([]byte{}); f != FormatOpenAI {
		t.Errorf("expected openai for empty, got %s", f)
	}
}

func TestDetect_InvalidJSON(t *testing.T) {
	body := []byte(`not json`)
	if f := Detect(body); f != FormatOpenAI {
		t.Errorf("expected openai for invalid json, got %s", f)
	}
}

func TestExtractModelID(t *testing.T) {
	tests := []struct {
		body string
		want string
	}{
		{`{"model":"gpt-4"}`, "gpt-4"},
		{`{"model":"claude-sonnet-4"}`, "claude-sonnet-4"},
		{`{"contents":[{"parts":[{"text":"hi"}]}]}`, ""},
		{`{}`, ""},
		{``, ""},
	}
	for _, tt := range tests {
		got := ExtractModelID([]byte(tt.body))
		if got != tt.want {
			t.Errorf("ExtractModelID(%q) = %q, want %q", tt.body, got, tt.want)
		}
	}
}

func TestIsStreaming(t *testing.T) {
	tests := []struct {
		body   string
		format Format
		want   bool
	}{
		{`{"stream":true}`, FormatOpenAI, true},
		{`{"stream":false}`, FormatOpenAI, false},
		{`{}`, FormatOpenAI, false},
		{`{"stream":true}`, FormatClaude, true},
	}
	for _, tt := range tests {
		got := IsStreaming([]byte(tt.body), tt.format)
		if got != tt.want {
			t.Errorf("IsStreaming(%q, %s) = %v, want %v", tt.body, tt.format, got, tt.want)
		}
	}
}

func TestIsLineData(t *testing.T) {
	if !isLineData("data: hello") {
		t.Error("expected true for 'data: hello'")
	}
	if isLineData("event: message") {
		t.Error("expected false for 'event: message'")
	}
}

func TestExtractData(t *testing.T) {
	got := extractData("data: {\"key\":\"val\"}\n")
	want := `{"key":"val"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
