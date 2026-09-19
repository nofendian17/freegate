package upstream

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// Stub tools injected into Zen requests that carry no tools.
//
// Root cause (verified live 2026-09-18 across /chat/completions,
// /messages, and /responses, then re-verified after the gate tightened
// the same day): the free-tier gate rejects anonymous requests whose
// body lacks tools with 403 FreeTierError. The gate does not just check
// presence — bisection showed it requires the default coding-agent tool
// names. Census of the genuine toolset lives in anomalyco/opencode
// packages/opencode/src/tool/registry.ts (builtin list); the first 11
// below are the always-sent wire order, followed by the conditional
// tools (question/apply_patch/execute/lsp/plan_exit — sent by genuine
// only when their flags apply). The never-sent `invalid` placeholder is
// deliberately excluded.
//
// The injection mirrors what a genuine agent session carries, in the
// native shape of each endpoint. Descriptions steer the model away from
// calling the stubs (genuine-client _noop precedent). It applies to
// anonymous requests only: keyed requests bypass the requirement and
// keep exact passthrough. If a model ever calls a stub, the tool_call
// passes through to the downstream client untouched.
var gateToolNames = []string{
	"bash", "edit", "glob", "grep", "read", "skill", "task",
	"todowrite", "webfetch", "websearch", "write",
	// Conditional tools, appended after the verified always-sent prefix.
	"question", "apply_patch", "execute", "lsp", "plan_exit",
}

const stubToolDescription = "Do not call this tool. It exists only for API compatibility and must never be invoked."

// ensureUpstreamTools guarantees the gate toolset is present in the
// endpoint's native shape. Bodies without tools (absent/null/empty array)
// get the full stub set; bodies that already carry tools keep them and
// gain only the missing gate names appended. A present-but-malformed
// value (string/number/object) or unparseable JSON passes through
// untouched so upstream returns its own clear 4xx instead of us
// silently replacing caller intent.
func ensureUpstreamTools(body []byte, endpoint string) []byte {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	t, ok := raw["tools"]
	if !ok || t == nil {
		raw["tools"] = gateStubs(endpoint)
		return marshalTools(body, raw, endpoint, len(gateToolNames), "injected")
	}
	arr, ok := t.([]any)
	if !ok {
		return body
	}
	if len(arr) == 0 {
		raw["tools"] = gateStubs(endpoint)
		return marshalTools(body, raw, endpoint, len(gateToolNames), "injected")
	}
	// Non-empty: append only the gate names the caller lacks (verified
	// live 2026-09-19: a /responses body with only foreign Hermes tools
	// drew 403 FreeTierError while the same model with gate tools passed).
	have := make(map[string]bool, len(arr))
	for _, item := range arr {
		if name := upstreamToolName(item); name != "" {
			have[name] = true
		}
	}
	var missing []string
	for _, name := range gateToolNames {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return body
	}
	merged := make([]any, 0, len(arr)+len(missing))
	merged = append(merged, arr...)
	for _, name := range missing {
		merged = append(merged, gateStub(endpoint, name))
	}
	raw["tools"] = merged
	return marshalTools(body, raw, endpoint, len(missing), "merged")
}

// upstreamToolName extracts the tool name from any endpoint shape:
// Claude/Responses ({name}) or OpenAI chat ({function:{name}}).
func upstreamToolName(item any) string {
	m, _ := item.(map[string]any)
	if m == nil {
		return ""
	}
	if fn, ok := m["function"].(map[string]any); ok {
		if name, _ := fn["name"].(string); name != "" {
			return name
		}
	}
	name, _ := m["name"].(string)
	return name
}

// gateStub builds one stub tool in the endpoint's native shape.
func gateStub(endpoint, name string) any {
	switch {
	case strings.HasSuffix(endpoint, "/messages"):
		return map[string]any{
			"name":        name,
			"description": stubToolDescription,
			"input_schema": map[string]any{
				"type": "object",
			},
		}
	case strings.HasSuffix(endpoint, "/responses"):
		return map[string]any{
			"type":        "function",
			"name":        name,
			"description": stubToolDescription,
			"parameters": map[string]any{
				"type": "object",
			},
			// Responses-family providers hardcode strict:false so
			// non-constrained schemas register (genuine client parity).
			"strict": false,
		}
	default:
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": stubToolDescription,
				"parameters": map[string]any{
					"type": "object",
				},
			},
		}
	}
}

// gateStubs builds the full stub set in the endpoint's native shape.
func gateStubs(endpoint string) []any {
	tools := make([]any, 0, len(gateToolNames))
	for _, name := range gateToolNames {
		tools = append(tools, gateStub(endpoint, name))
	}
	return tools
}

func marshalTools(body []byte, raw map[string]any, endpoint string, count int, action string) []byte {
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	slog.Debug("opencode stub tools "+action, "endpoint", endpoint, "count", count)
	return out
}
