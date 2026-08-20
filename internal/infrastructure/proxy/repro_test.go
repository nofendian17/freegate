package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"freegate/internal/translate/claude"
)

func reproDump(t *testing.T, name string, input string) string {
	t.Helper()
	var out bytes.Buffer
	normalizeOpenAIStream(&out, bufio.NewReader(strings.NewReader(input)))
	fmt.Printf("===== SCENARIO: %s =====\n", name)
	fmt.Printf("--- OUTPUT ---\n%s--- END OUTPUT ---\n", out.String())
	return out.String()
}

// Feed the raw client-visible tool_calls from the output stream to the
// claude translator (OpenAI -> Claude) to see the Claude SSE events.
func reproClaude(t *testing.T, name string, openaiStream string) string {
	t.Helper()
	state := claude.NewStreamState()
	var events []string
	rd := bufio.NewReader(strings.NewReader(openaiStream))
	for {
		line, err := rd.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data: ") && !strings.HasPrefix(line, "data: [DONE]") {
			data := strings.TrimPrefix(line, "data: ")
			var chunk map[string]any
			if json.Unmarshal([]byte(data), &chunk) == nil {
				events = append(events, claude.ProcessChunk(chunk, state)...)
			}
		}
		if err != nil {
			break
		}
	}
	fmt.Printf("===== CLAUDE EVENTS: %s =====\n", name)
	for _, e := range events {
		fmt.Print(e)
	}
	fmt.Println("===== END CLAUDE EVENTS =====")
	return strings.Join(events, "")
}

func TestReproLeaks(t *testing.T) {
	// Scenario 1: user's exact error - two concatenated Read objects in one delta.
	args := `{"file_path":"/tmp/claude-1000/-home-beni-Projects-go-lab-freegate/3f161ac3-2aea-4e58-a6c0-b3efecb9bb0c/tasks/a07415b3130822db2.output"}{"file_path":"/tmp/claude-1000/-home-beni-Projects-go-lab-freegate/3f161ac3-2aea-4e58-a6c0-b3efecb9bb0c/tasks/b2.output"}`
	tc1 := fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":%q}}]},"finish_reason":null}]}`, args)
	in1 := "data: " + tc1 + "\n\n"
	in1 += `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"
	in1 += "data: [DONE]\n\n"
	o1 := reproDump(t, "1-concat-in-single-delta", in1)
	reproClaude(t, "1-concat-in-single-delta", o1)

	// Scenario 2: args split across two deltas that concatenate to the bad string.
	half := len(args) / 2
	in2 := "data: " + fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":%q}}]},"finish_reason":null}]}`, args[:half]) + "\n\n"
	in2 += "data: " + fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":%q}}]},"finish_reason":null}]}`, args[half:]) + "\n\n"
	in2 += `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"
	in2 += "data: [DONE]\n\n"
	o2 := reproDump(t, "2-concat-split-across-deltas", in2)
	reproClaude(t, "2-concat-split-across-deltas", o2)

	// Scenario 3: tool_calls placed under `message` instead of `delta`
	// (some free-tier gateways emit non-streaming shape in a stream).
	in3 := "data: " + `{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"` + args + `"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"
	in3 += "data: [DONE]\n\n"
	o3 := reproDump(t, "3-tool_calls-under-message", in3)
	reproClaude(t, "3-tool_calls-under-message", o3)

	// Scenario 4: tool_calls as single object, not array.
	tc4 := fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":%q}}},"finish_reason":null}]}`, args)
	in4 := "data: " + tc4 + "\n\n"
	in4 += `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"
	in4 += "data: [DONE]\n\n"
	o4 := reproDump(t, "4-tool_calls-single-object", in4)
	reproClaude(t, "4-tool_calls-single-object", o4)

	// Scenario 5: TWO separate tool calls same index, both complete Read objects.
	in5 := "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"A\"}"}}]},"finish_reason":null}]}` + "\n\n"
	in5 += "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"B\"}"}}]},"finish_reason":null}]}` + "\n\n"
	in5 += `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"
	in5 += "data: [DONE]\n\n"
	o5 := reproDump(t, "5-two-complete-calls-same-index", in5)
	reproClaude(t, "5-two-complete-calls-same-index", o5)

	// Scenario 6: two tool calls, different indices, complete objects.
	in6 := "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"A\"}"}}]},"finish_reason":null}]}` + "\n\n"
	in6 += "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"B\"}"}}]},"finish_reason":null}]}` + "\n\n"
	in6 += `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"
	in6 += "data: [DONE]\n\n"
	o6 := reproDump(t, "6-two-calls-different-index", in6)
	reproClaude(t, "6-two-calls-different-index", o6)
}
