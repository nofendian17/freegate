package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

const assembleChatSSE = `data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","created":1789658800,"model":"mimo-v2.5-free","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","created":1789658800,"model":"mimo-v2.5-free","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","created":1789658800,"model":"mimo-v2.5-free","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","created":1789658800,"model":"mimo-v2.5-free","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","created":1789658800,"model":"mimo-v2.5-free","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"la\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-abc","object":"chat.completion.chunk","created":1789658800,"model":"mimo-v2.5-free","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":8,"completion_tokens":5,"total_tokens":13}}

data: [DONE]
`

const assembleClaudeSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"union-alpha","content":[],"usage":{"input_tokens":8,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"la\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}
`

const assembleResponsesSSE = `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"in_progress","output":[]}}

event: response.completed
data: {"type":"response.completed","sequence_number":9,"response":{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","id":"msg_0","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]},"usage":{"input_tokens":8,"output_tokens":5,"total_tokens":13}}
`

func TestAssembleChatCompletion(t *testing.T) {
	out, err := assembleUpstreamStream("/chat/completions", strings.NewReader(assembleChatSSE), "mimo-v2.5-free", MaxResponseBodySize)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	var resp struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role      string `json:"role"`
				Content   any    `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if resp.Object != "chat.completion" || resp.Model != "mimo-v2.5-free" {
		t.Fatalf("envelope broken: %s", out)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices: %s", out)
	}
	msg := resp.Choices[0].Message
	if msg.Role != "assistant" || msg.Content != "hello" {
		t.Fatalf("message: %+v", msg)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("tool_calls: %+v", msg.ToolCalls)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(msg.ToolCalls[0].Function.Arguments), &args); err != nil || args["city"] != "la" {
		t.Fatalf("arguments not joined+valid: %q", msg.ToolCalls[0].Function.Arguments)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason: %q", resp.Choices[0].FinishReason)
	}
	if resp.Usage["total_tokens"] != float64(13) {
		t.Fatalf("usage: %v", resp.Usage)
	}
}

func TestAssembleChatCompletion_PreservesReasoning(t *testing.T) {
	sse := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"thinking \"}}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"hard\",\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	out, err := assembleUpstreamStream("/chat/completions", strings.NewReader(sse), "m", MaxResponseBodySize)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	msg := resp.Choices[0].Message
	if msg.Content != "hi" || msg.Reasoning != "thinking hard" {
		t.Fatalf("got %+v", msg)
	}
}

func TestAssembleClaudeMessage(t *testing.T) {
	out, err := assembleUpstreamStream("/messages", strings.NewReader(assembleClaudeSSE), "union-alpha", MaxResponseBodySize)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	var resp struct {
		Type       string `json:"type"`
		Role       string `json:"role"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if resp.Type != "message" || resp.Role != "assistant" || resp.StopReason != "tool_use" {
		t.Fatalf("envelope: %s", out)
	}
	if len(resp.Content) != 2 || resp.Content[0].Text != "hello" {
		t.Fatalf("text block: %+v", resp.Content)
	}
	tu := resp.Content[1]
	if tu.Type != "tool_use" || tu.Name != "get_weather" || tu.Input["city"] != "la" {
		t.Fatalf("tool_use block: %+v", tu)
	}
	if resp.Usage["input_tokens"] != float64(8) || resp.Usage["output_tokens"] != float64(5) {
		t.Fatalf("usage: %v", resp.Usage)
	}
}

func TestAssembleResponsesObject_IncompleteFallsBack(t *testing.T) {
	sse := "event: response.incomplete\n" +
		"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_x\",\"object\":\"response\",\"status\":\"incomplete\",\"output\":[]}}\n\n"
	out, err := assembleUpstreamStream("/responses", strings.NewReader(sse), "muse", MaxResponseBodySize)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	var resp struct {
		Object string `json:"object"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if resp.Object != "response" || resp.Status != "incomplete" {
		t.Fatalf("got %s", out)
	}
}

func TestAssembleResponsesObject(t *testing.T) {
	out, err := assembleUpstreamStream("/responses", strings.NewReader(assembleResponsesSSE), "muse-spark", MaxResponseBodySize)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	var resp struct {
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if resp.Object != "response" || resp.Status != "completed" {
		t.Fatalf("envelope: %s", out)
	}
	if len(resp.Output) != 1 || resp.Output[0].Content[0].Text != "hello" {
		t.Fatalf("output: %s", out)
	}
}

func TestAssembleStream_TruncatedIsBestEffort(t *testing.T) {
	// Stream cut mid-flight still yields a usable partial completion.
	out, err := assembleUpstreamStream("/chat/completions", strings.NewReader("data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n"), "m", MaxResponseBodySize)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if !strings.Contains(string(out), `"content":"hi"`) {
		t.Fatalf("partial lost: %s", out)
	}
}
