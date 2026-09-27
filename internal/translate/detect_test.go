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
	// max_tokens alone is no longer sufficient to detect Claude (OpenAI also
	// uses max_tokens). Without other Claude indicators (anthropic_version,
	// top-level system, or Claude content types), this defaults to OpenAI.
	if f := Detect(body); f != FormatOpenAI {
		t.Errorf("expected openai (max_tokens alone is ambiguous), got %s", f)
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

func TestDetect_Responses_Basic(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.2-contributor-free","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	if f := Detect(body); f != Format("openai-responses") {
		t.Errorf("expected openai-responses, got %s", f)
	}
}

func TestDetect_Responses_StringInput(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.2-contributor-free","input":"hello"}`)
	if f := Detect(body); f != Format("openai-responses") {
		t.Errorf("expected openai-responses for string input, got %s", f)
	}
}

func TestDetect_Responses_EmptyArray(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.2-contributor-free","input":[]}`)
	if f := Detect(body); f != Format("openai-responses") {
		t.Errorf("expected openai-responses for empty input, got %s", f)
	}
}

func TestProbeBodyMatchesDetect(t *testing.T) {
	bodies := []string{
		`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`,
		`{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]}]}`,
		`{"model":"claude-sonnet-4","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"claude-sonnet-4","system":"You are helpful","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"claude-sonnet-4","system":[{"type":"text","text":"You are helpful"}],"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"claude-sonnet-4","max_tokens":100,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"/9j/4AAQ"}}]}]}`,
		`{"model":"claude-sonnet-4","max_tokens":100,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"get_weather","input":{"city":"NYC"}}]}]}`,
		`{"model":"claude-sonnet-4","max_tokens":100,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"sunny"}]}]}`,
		`{"anthropic_version":"bedrock-2023-05-31","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`,
		`{"contents":[{"parts":[{"text":"hi"}],"role":"user"}]}`,
		`{"contents":[{"parts":[{"text":"hello"}],"role":"user"}],"generationConfig":{"temperature":0.7}}`,
		`{"model":"muse-spark-1.2-contributor-free","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
		`{"model":"muse-spark-1.2-contributor-free","input":"hello"}`,
		`{"model":"muse-spark-1.2-contributor-free","input":[]}`,
		`{"model":"gpt-4"}`,
		`{"contents":[{"parts":[{"text":"hi"}]}]}`,
		`{}`,
		``,
		`not json`,
		`{"model":"x","messages":[],"system":"s"}`,
		`{"model":"x","messages":null}`,
		`{"model":"x","input":null}`,
		`{"model":"x","system":42,"messages":[{"role":"user","content":"hi"}]}`,
	}
	for _, tc := range bodies {
		body := []byte(tc)
		wantFormat := Detect(body)
		wantModel := ExtractModelID(body)
		gotFormat, gotModel := ProbeBody(body)
		if gotFormat != wantFormat || gotModel != wantModel {
			t.Errorf("ProbeBody(%q) = (%s,%q), want (%s,%q)", tc, gotFormat, gotModel, wantFormat, wantModel)
		}
	}
}

func TestProbeByPath(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if f, m := ProbeByPath("/v1/responses", body); f != FormatOpenAIResponses || m != "m" {
		t.Errorf("responses path = (%s,%q)", f, m)
	}
	if f, m := ProbeByPath("/v1/messages", body); f != FormatClaude || m != "m" {
		t.Errorf("messages path = (%s,%q)", f, m)
	}
	if f, m := ProbeByPath("/v1/chat/completions", body); f != FormatOpenAI || m != "m" {
		t.Errorf("chat path = (%s,%q)", f, m)
	}
}

var benchOpenAIBody = []byte(`{"model":"gpt-4o","messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"Explain allocation reuse in Go slice appends with an example"}],"temperature":0.7,"stream":true}`)

func BenchmarkDetectPlusExtract(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Detect(benchOpenAIBody)
		_ = ExtractModelID(benchOpenAIBody)
	}
}

func BenchmarkProbeBody(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = ProbeBody(benchOpenAIBody)
	}
}
