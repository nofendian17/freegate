package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"freegate/internal/translate/claude"
)

// TestNormalizeStream_MissingFinishReasonEOF_PreservesToolArgs reproduces the
// Claude Code failure "InputValidationError: Bash failed due to the following
// issue: The required parameter `command` is missing".
//
// Scenario (muse-spark-1.2-contributor-free): the upstream streams Bash
// tool-call arguments, then closes the connection via EOF — no [DONE], no
// real finish_reason. bufferToolArgs strips the incremental arguments from
// every delta and buffers them; the EOF fallback synthesizes a terminal
// chunk but never calls emitRepaired, so the buffered arguments are dropped
// and the translated tool_use block reaches Claude Code with input {}.
//
// Contract: the repaired arguments must appear in the normalized stream AND
// survive translation into a Claude input_json_delta before the terminal
// message_stop.
func TestNormalizeStream_MissingFinishReasonEOF_PreservesToolArgs(t *testing.T) {
	input := "" +
		"data: {\"id\":\"c1\",\"model\":\"muse-spark\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"Bash\",\"arguments\":\"{\\\"command\\\":\\\"ls -la\\\"}\"}}]},\"finish_reason\":null}]}\n" +
		"data: {\"id\":\"c1\",\"model\":\"muse-spark\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null}]}\n"

	var buf bytes.Buffer
	normalizeOpenAIStream(&buf, bufio.NewReader(strings.NewReader(input)))

	normalized := buf.String()
	// Arguments ride inside a JSON string value, so they appear escaped.
	if !strings.Contains(normalized, `{\"command\":\"ls -la\"}`) {
		t.Fatalf("normalized stream lost buffered tool arguments, got:\n%s", normalized)
	}

	// Translate the normalized OpenAI SSE into Claude SSE, like the
	// ResponseWriter does, and verify the arguments arrive intact.
	state := claude.NewStreamState()
	var events []string
	rd := bufio.NewReader(strings.NewReader(normalized))
	for {
		line, err := rd.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if data, ok := strings.CutPrefix(line, "data: "); ok && data != "[DONE]" {
			var chunk map[string]any
			if json.Unmarshal([]byte(data), &chunk) == nil {
				events = append(events, claude.ProcessChunk(chunk, state)...)
			}
		}
		if err != nil {
			break
		}
	}

	joined := strings.Join(events, "")
	if !strings.Contains(joined, "input_json_delta") {
		t.Fatalf("no input_json_delta emitted for tool_use block, got:\n%s", joined)
	}
	if !strings.Contains(joined, "ls -la") {
		t.Fatalf("tool arguments missing from Claude events, got:\n%s", joined)
	}
	if !strings.Contains(joined, "message_stop") {
		t.Fatalf("stream did not terminate with message_stop, got:\n%s", joined)
	}
}
