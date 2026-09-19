package prepost

// DSML tool-call stop (vllm#54686 port).
//
// A tool call ends the turn: handing the DSML tool_calls closer to the
// sampler as a stop string ends generation where it logically ends,
// instead of letting DeepSeek-V4 re-emit blocks until max_tokens
// (upstream observed 32k tokens / ~400s of repeats in production).
//
// Mirrors vLLM _add_tool_call_stop: absent stop gets [closer], a string
// stop becomes [stop, closer], an array gets the closer appended — all
// idempotent when the closer is already present. One proxy-specific
// guard on top: a full stop array is left alone, since a 5th entry
// risks an upstream 400.

// DSMLToolCallsCloser is the literal that closes a DSML tool-call block
// (vLLM DSML_TOOL_END, fullwidth pipes U+FF5C).
const DSMLToolCallsCloser = "</｜DSML｜tool_calls>"

// maxOpenAIStops is the OpenAI limit on stop strings per request.
const maxOpenAIStops = 4

// ensureDSMLToolStop appends DSMLToolCallsCloser to the OpenAI-shaped
// body's stop field. A malformed non-string/array value is left for the
// upstream to reject. Reports whether anything changed.
func ensureDSMLToolStop(raw map[string]any) bool {
	switch stop := raw["stop"].(type) {
	case nil:
		raw["stop"] = []any{DSMLToolCallsCloser}
		return true
	case string:
		if stop == DSMLToolCallsCloser {
			return false
		}
		raw["stop"] = []any{stop, DSMLToolCallsCloser}
		return true
	case []any:
		for _, s := range stop {
			if v, _ := s.(string); v == DSMLToolCallsCloser {
				return false
			}
		}
		if len(stop) >= maxOpenAIStops {
			return false
		}
		raw["stop"] = append(stop, DSMLToolCallsCloser)
		return true
	default:
		return false
	}
}
