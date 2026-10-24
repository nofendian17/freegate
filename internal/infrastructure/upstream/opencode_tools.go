package upstream

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
)

// Fingerprint tools required by the OpenCode free-tier gate.
//
// Mirrors 9router PR #4842 (fix/opencode-free-inference-v2): live probes
// against /inference/v1 show free-tier requests must declare OpenCode's own
// `read` and `shell` tools with their exact descriptions and parameter
// schemas (verbatim from the v2.0.26 CLI). Stubbed descriptions, renamed
// schemas, or tool_choice "none" are answered with 403 FreeTierError. The
// gate also requires an OpenCode-style <env> block in the system prompt;
// see ensureOpenCodeEnv.
//
// Caller tools pass through verbatim, except one that already uses an exact
// fingerprint name with a different definition: it travels under an alias
// (`read_client`, `shell_client`, ...) including its prior calls in history,
// and the response side restores the original name. Case variants such as
// Claude Code's Read/Bash pass through untouched; upstream accepts them next
// to read/shell.
var fingerprintToolNames = []string{"read", "shell"}

// Verbatim from the OpenCode v2.0.26 CLI. Upstream compares description and
// parameters, so do not reword or reformat these (concatenation only).
const fingerprintReadDescription = "Read the contents of a file or directory. " +
	"Supports text files, images, and PDFs. " +
	"Images and PDFs are presented directly to the model. " +
	"Each text line is prefixed by its 1-based line number as <line>: <content>. " +
	"The prefix is for reference and is not part of the file content. " +
	"Directory entries are returned one per line. " +
	"Use offset and limit to read large files or directories in sections. " +
	"Prefer one larger read over many small slices, " +
	"and use grep to find specific content in large files."

const fingerprintShellDescription = "Execute a shell command and return its output. " +
	"Commands run on Linux using bash. " +
	"Quote file paths containing spaces or special characters. " +
	"Prefer dedicated tools over shell commands when possible. " +
	"When output is large, the full result is saved to a file and a truncated preview is returned. " +
	"Rely on automatic truncation unless filtering the output is more useful. " +
	"Commands accept an optional timeout, background commands have no timeout by default. " +
	"Background commands return immediately, and you will be notified when they complete."

const (
	fingerprintReadOffsetDesc = "The line or directory entry to start reading from (1-based)"
	fingerprintReadLimitDesc  = "The maximum number of lines or directory entries to read " +
		"(defaults to and capped at 2000)"
	fingerprintShellWorkdirDesc = "Working directory to execute the command in. " +
		"Defaults to the current working directory. When possible, avoid changing directories " +
		"in the command and set the working directory here instead."
	fingerprintShellTimeoutDesc = "Timeout in milliseconds. Set to 0 to disable the timeout. " +
		"Defaults to 120000 for foreground commands. Background commands have no timeout by default."
	fingerprintShellBackgroundDesc = "Run the command in the background and return immediately " +
		"(useful for dev servers and long-running builds). You do not need to use '&' at the end " +
		"of the command when using this parameter. You will be notified when it completes. " +
		"DO NOT poll for completion."
)

func fingerprintReadParameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "File or directory to read"},
			"offset": map[string]any{"type": "integer", "minimum": 0, "description": fingerprintReadOffsetDesc},
			"limit":  map[string]any{"type": "integer", "minimum": 0, "description": fingerprintReadLimitDesc},
		},
		"required":             []any{"path"},
		"additionalProperties": false,
	}
}

func fingerprintShellParameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":    map[string]any{"type": "string", "description": "Shell command string to execute"},
			"workdir":    map[string]any{"type": "string", "description": fingerprintShellWorkdirDesc},
			"timeout":    map[string]any{"type": "integer", "minimum": 0, "description": fingerprintShellTimeoutDesc},
			"background": map[string]any{"type": "boolean", "description": fingerprintShellBackgroundDesc},
		},
		"required":             []any{"command"},
		"additionalProperties": false,
	}
}

func fingerprintWant(name string) (string, map[string]any) {
	if name == "read" {
		return fingerprintReadDescription, fingerprintReadParameters()
	}
	return fingerprintShellDescription, fingerprintShellParameters()
}

// fingerprintToolKey returns the fingerprint name when name is exactly one;
// otherwise "". Matching is case-sensitive: Read/Bash pass through untouched.
func fingerprintToolKey(name string) string {
	trimmed := strings.TrimSpace(name)
	for _, n := range fingerprintToolNames {
		if trimmed == n {
			return n
		}
	}
	return ""
}

// upstreamToolName extracts the tool name from any endpoint shape:
// Claude/Responses ({name}) or OpenAI chat ({function:{name}}).
func upstreamToolName(item any) string {
	m, _ := item.(map[string]any)
	if m == nil {
		return ""
	}
	if fn, ok := m["function"].(map[string]any); ok {
		if name, _ := fn["name"].(string); strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	name, _ := m["name"].(string)
	return strings.TrimSpace(name)
}

func toolDescriptionOf(item any) string {
	m, _ := item.(map[string]any)
	if m == nil {
		return ""
	}
	if fn, ok := m["function"].(map[string]any); ok {
		if d, _ := fn["description"].(string); d != "" {
			return d
		}
	}
	d, _ := m["description"].(string)
	return d
}

// toolParametersOf returns the parameter schema regardless of shape:
// Responses/chat use parameters (or function.parameters), Messages uses
// input_schema.
func toolParametersOf(item any) any {
	m, _ := item.(map[string]any)
	if m == nil {
		return nil
	}
	if fn, ok := m["function"].(map[string]any); ok {
		if p, ok := fn["parameters"]; ok {
			return p
		}
	}
	if p, ok := m["parameters"]; ok {
		return p
	}
	if s, ok := m["input_schema"]; ok {
		return s
	}
	return nil
}

// canonicalJSON normalizes a value through JSON round-trip so DeepEqual
// ignores Go map ordering and number-type differences.
func canonicalJSON(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return v
	}
	return out
}

// isFingerprintDefinition reports whether tool already carries OpenCode's
// exact definition for key.
func isFingerprintDefinition(tool any, key string) bool {
	wantDesc, wantParams := fingerprintWant(key)
	if toolDescriptionOf(tool) != wantDesc {
		return false
	}
	return reflect.DeepEqual(canonicalJSON(toolParametersOf(tool)), canonicalJSON(wantParams))
}

func withToolName(tool any, name string) any {
	m, ok := tool.(map[string]any)
	if !ok || m == nil {
		return tool
	}
	if fn, ok := m["function"].(map[string]any); ok {
		next := make(map[string]any, len(m))
		for k, v := range m {
			next[k] = v
		}
		fnCopy := make(map[string]any, len(fn))
		for k, v := range fn {
			fnCopy[k] = v
		}
		fnCopy["name"] = name
		next["function"] = fnCopy
		return next
	}
	next := make(map[string]any, len(m))
	for k, v := range m {
		next[k] = v
	}
	next["name"] = name
	return next
}

// concealFingerprintToolNames moves caller tools off the fingerprint names
// when their definition differs from OpenCode's, so the exact definitions
// can be appended without a duplicate name. Returns the rewritten list and
// a sent-name -> original-name map.
func concealFingerprintToolNames(tools []any) ([]any, map[string]string) {
	aliasToOriginal := make(map[string]string)
	if len(tools) == 0 {
		return tools, aliasToOriginal
	}
	taken := make(map[string]bool, len(tools))
	for _, t := range tools {
		if n := upstreamToolName(t); n != "" {
			taken[n] = true
		}
	}
	seen := make(map[string]bool)
	out := make([]any, 0, len(tools)+len(fingerprintToolNames))
	for _, tool := range tools {
		m, ok := tool.(map[string]any)
		if !ok || m == nil {
			out = append(out, tool)
			continue
		}
		key := fingerprintToolKey(upstreamToolName(tool))
		if key == "" {
			out = append(out, tool)
			continue
		}
		// Duplicate declarations of one name are rejected upstream; keep the first.
		if seen[key] {
			continue
		}
		seen[key] = true
		if isFingerprintDefinition(tool, key) {
			out = append(out, tool)
			continue
		}
		alias := key + "_client"
		for i := 2; taken[alias]; i++ {
			alias = key + "_client" + strconv.Itoa(i)
		}
		taken[alias] = true
		aliasToOriginal[alias] = key
		out = append(out, withToolName(tool, alias))
	}
	return out, aliasToOriginal
}

// fingerprintStub builds one exact fingerprint tool in the endpoint's native
// shape.
func fingerprintStub(endpoint, name string) any {
	desc, params := fingerprintWant(name)
	switch {
	case strings.HasSuffix(endpoint, "/messages"):
		return map[string]any{
			"name":         name,
			"description":  desc,
			"input_schema": params,
		}
	case strings.HasSuffix(endpoint, "/responses"):
		return map[string]any{
			"type":        "function",
			"name":        name,
			"description": desc,
			"parameters":  params,
			"strict":      false,
		}
	default:
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": desc,
				"parameters":  params,
			},
		}
	}
}

// appendMissingFingerprintTools appends OpenCode's definition for each
// fingerprint tool not already declared. Returns the appended names.
func appendMissingFingerprintTools(tools []any, endpoint string) ([]any, []string) {
	var appended []string
	for _, name := range fingerprintToolNames {
		found := false
		for _, t := range tools {
			if fingerprintToolKey(upstreamToolName(t)) == name {
				found = true
				break
			}
		}
		if !found {
			tools = append(tools, fingerprintStub(endpoint, name))
			appended = append(appended, name)
		}
	}
	return tools, appended
}

// aliasesByOriginal inverts a sent -> original rename map.
func aliasesByOriginal(aliasToOriginal map[string]string) map[string]string {
	out := make(map[string]string, len(aliasToOriginal))
	for alias, original := range aliasToOriginal {
		out[original] = alias
	}
	return out
}

// retargetToolChoice points an explicit tool_choice at the alias of a
// concealed caller tool.
func retargetToolChoice(raw map[string]any, aliasToOriginal map[string]string) {
	if len(aliasToOriginal) == 0 {
		return
	}
	choice, ok := raw["tool_choice"].(map[string]any)
	if !ok {
		return
	}
	byOriginal := aliasesByOriginal(aliasToOriginal)
	if name, _ := choice["name"].(string); strings.TrimSpace(name) != "" {
		if alias, ok := byOriginal[strings.TrimSpace(name)]; ok {
			choice["name"] = alias
		}
		return
	}
	if fn, ok := choice["function"].(map[string]any); ok {
		if name, _ := fn["name"].(string); strings.TrimSpace(name) != "" {
			if alias, ok := byOriginal[strings.TrimSpace(name)]; ok {
				fn["name"] = alias
			}
		}
	}
}

// aliasHistoryToolCalls renames prior-turn calls to concealed caller tools;
// otherwise the model reads its own earlier calls as calls to OpenCode's
// tool of the same name.
func aliasHistoryToolCalls(raw map[string]any, aliasToOriginal map[string]string) {
	if len(aliasToOriginal) == 0 {
		return
	}
	byOriginal := aliasesByOriginal(aliasToOriginal)
	if arr, ok := raw["input"].([]any); ok {
		for i, item := range arr {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			if m["type"] == "function_call" {
				if name, _ := m["name"].(string); name != "" {
					if alias, ok := byOriginal[strings.TrimSpace(name)]; ok {
						m["name"] = alias
						arr[i] = m
					}
				}
			}
		}
	}
	if arr, ok := raw["messages"].([]any); ok {
		for i, item := range arr {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			tcs, ok := m["tool_calls"].([]any)
			if !ok {
				continue
			}
			changed := false
			for j, tc := range tcs {
				tcm, _ := tc.(map[string]any)
				if tcm == nil {
					continue
				}
				fn, _ := tcm["function"].(map[string]any)
				if fn == nil {
					continue
				}
				if name, _ := fn["name"].(string); name != "" {
					if alias, ok := byOriginal[strings.TrimSpace(name)]; ok {
						fn["name"] = alias
						tcm["function"] = fn
						tcs[j] = tcm
						changed = true
					}
				}
			}
			if changed {
				m["tool_calls"] = tcs
				arr[i] = m
			}
		}
	}
}

// Without stub "must not be used" descriptions the model does call the
// appended tools (live probe: `read` on "what files are in /etc?"), so tell
// it in the system prompt instead. Upstream gates on the <env> marker, not
// on this note's wording: "not available" made the model also refuse the
// caller's own Read/Bash; "placeholders ... use the other tools" kept those
// working.
func noteUnavailableTools(raw map[string]any, endpoint string, names []string) {
	if len(names) == 0 {
		return
	}
	var list string
	if len(names) == 1 {
		list = "`" + names[0] + "`"
	} else {
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = "`" + n + "`"
		}
		list = strings.Join(quoted, " or ")
	}
	var note string
	if len(names) > 1 {
		note = "Do not call the " + list + " tools; they are placeholders that do nothing. " +
			"Use the other tools you have been given instead."
	} else {
		note = "Do not call the " + list + " tool; it is a placeholder that does nothing. " +
			"Use the other tools you have been given instead."
	}
	if strings.HasSuffix(endpoint, "/responses") {
		if s, _ := raw["instructions"].(string); strings.TrimSpace(s) != "" {
			raw["instructions"] = s + "\n\n" + note
		} else {
			raw["instructions"] = note
		}
		return
	}
	if strings.HasSuffix(endpoint, "/messages") {
		switch s := raw["system"].(type) {
		case string:
			raw["system"] = appendPromptBlock(s, note)
		case []any:
			raw["system"] = append(s, map[string]any{"type": "text", "text": note})
		default:
			raw["system"] = note
		}
		return
	}
	// OpenAI chat shape only; Claude-shaped bodies carry `system` instead.
	if _, hasSystem := raw["system"]; hasSystem {
		return
	}
	msgs, ok := raw["messages"].([]any)
	if !ok {
		return
	}
	if len(msgs) > 0 {
		if first, ok := msgs[0].(map[string]any); ok && first["role"] == "system" {
			switch content := first["content"].(type) {
			case string:
				first["content"] = appendPromptBlock(content, note)
			case []any:
				first["content"] = append(content, map[string]any{"type": "text", "text": note})
			case nil:
				first["content"] = note
			default:
				raw["messages"] = append([]any{map[string]any{"role": "system", "content": note}}, msgs...)
				return
			}
			msgs[0] = first
			raw["messages"] = msgs
			return
		}
	}
	raw["messages"] = append([]any{map[string]any{"role": "system", "content": note}}, msgs...)
}

// ensureOpenCodeEnv guarantees an OpenCode-style <env> block in the
// request's system prompt. The free-tier gate requires more than the
// read/shell fingerprints: live probes show a body with exact tools but no
// <env> block is answered with 429 FreeUsageLimitError, while adding a
// minimal block (Working directory + Platform) to the same body restores
// 200. The block is a client-fingerprint marker, not a description of the
// caller's real environment; a real OpenCode client already sends its own
// and is left untouched.
func ensureOpenCodeEnv(raw map[string]any, endpoint string) bool {
	if raw == nil || hasOpenCodeEnv(raw) {
		return false
	}
	block := openCodeEnvBlock()
	switch {
	case strings.HasSuffix(endpoint, "/responses"):
		ins, ok := raw["instructions"]
		if !ok || ins == nil {
			raw["instructions"] = block
			return true
		}
		if s, ok := ins.(string); ok {
			raw["instructions"] = appendPromptBlock(s, block)
			return true
		}
		return false
	case strings.HasSuffix(endpoint, "/messages"):
		return appendSystemEnv(raw, block)
	default:
		if _, ok := raw["system"]; ok {
			return appendSystemEnv(raw, block)
		}
		return appendChatEnv(raw, block)
	}
}

// appendSystemEnv appends block to a top-level string/array `system` field,
// creating it when absent. Used by the Claude Messages shape and by chat
// bodies that carry a legacy top-level system prompt.
func appendSystemEnv(raw map[string]any, block string) bool {
	sys, ok := raw["system"]
	if !ok || sys == nil {
		raw["system"] = block
		return true
	}
	switch s := sys.(type) {
	case string:
		raw["system"] = appendPromptBlock(s, block)
		return true
	case []any:
		raw["system"] = append(s, map[string]any{"type": "text", "text": block})
		return true
	}
	return false
}

func appendChatEnv(raw map[string]any, block string) bool {
	msgs, ok := raw["messages"].([]any)
	if !ok {
		return false
	}
	if len(msgs) > 0 {
		if first, ok := msgs[0].(map[string]any); ok && first["role"] == "system" {
			switch content := first["content"].(type) {
			case string:
				first["content"] = appendPromptBlock(content, block)
			case []any:
				first["content"] = append(content, map[string]any{"type": "text", "text": block})
			case nil:
				first["content"] = block
			default:
				return false
			}
			msgs[0] = first
			raw["messages"] = msgs
			return true
		}
	}
	raw["messages"] = append([]any{map[string]any{"role": "system", "content": block}}, msgs...)
	return true
}

func hasOpenCodeEnv(raw map[string]any) bool {
	if promptValueHasEnv(raw["instructions"]) || promptValueHasEnv(raw["system"]) {
		return true
	}
	msgs, _ := raw["messages"].([]any)
	if len(msgs) > 0 {
		if first, ok := msgs[0].(map[string]any); ok && first["role"] == "system" {
			return promptValueHasEnv(first["content"])
		}
	}
	return false
}

func promptValueHasEnv(v any) bool {
	switch s := v.(type) {
	case string:
		return strings.Contains(s, "<env>")
	case []any:
		for _, part := range s {
			if m, ok := part.(map[string]any); ok {
				if text, ok := m["text"].(string); ok && strings.Contains(text, "<env>") {
					return true
				}
				continue
			}
			if text, ok := part.(string); ok && strings.Contains(text, "<env>") {
				return true
			}
		}
	}
	return false
}

func appendPromptBlock(existing, block string) string {
	if strings.TrimSpace(existing) == "" {
		return block
	}
	return strings.TrimRight(existing, "\r\n") + "\n\n" + block
}

func openCodeEnvBlock() string {
	wd := "/"
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		wd = cwd
	}
	return "<env>\n  Working directory: " + wd + "\n  Platform: " + runtime.GOOS + "\n</env>"
}

// stripNoneToolChoice drops a caller-supplied tool_choice "none": upstream
// answers it with 403 FreeTierError. Absent choice lets the model decide.
func stripNoneToolChoice(raw map[string]any) {
	tc, ok := raw["tool_choice"]
	if !ok || tc == nil {
		return
	}
	if s, ok := tc.(string); ok {
		if strings.EqualFold(strings.TrimSpace(s), "none") {
			delete(raw, "tool_choice")
		}
		return
	}
	if m, ok := tc.(map[string]any); ok {
		if t, _ := m["type"].(string); strings.EqualFold(strings.TrimSpace(t), "none") {
			delete(raw, "tool_choice")
		}
	}
}

// applyFingerprintTools is the full request-side pass: alias conflicting
// caller tools, append OpenCode's fingerprint definitions, and tell the
// model the appended ones are off-limits. Returns the alias map
// (sent-name -> original-name) for the response side.
func applyFingerprintTools(raw map[string]any, endpoint string) map[string]string {
	empty := make(map[string]string)
	if raw == nil {
		return empty
	}
	stripNoneToolChoice(raw)
	var tools []any
	hasTools := false
	if t, ok := raw["tools"]; ok && t != nil {
		if arr, ok := t.([]any); ok {
			tools = arr
			hasTools = true
		} else {
			// Present-but-malformed tools pass through for a clear
			// upstream 4xx instead of silently replacing caller intent.
			return empty
		}
	}
	concealed, aliasMap := concealFingerprintToolNames(tools)
	concealed, appended := appendMissingFingerprintTools(concealed, endpoint)
	raw["tools"] = concealed
	_ = hasTools
	retargetToolChoice(raw, aliasMap)
	aliasHistoryToolCalls(raw, aliasMap)
	if len(appended) > 0 {
		noteUnavailableTools(raw, endpoint, appended)
	}
	// Responses keeps its explicit auto default. Never default to "none":
	// upstream answers tool_choice "none" with 403 FreeTierError.
	if _, ok := raw["tool_choice"]; !ok && strings.HasSuffix(endpoint, "/responses") {
		raw["tool_choice"] = "auto"
	}
	if ensureOpenCodeEnv(raw, endpoint) {
		slog.Debug("opencode env fingerprint injected", "endpoint", endpoint)
	}
	return aliasMap
}

// ensureUpstreamTools guarantees the fingerprint toolset is present in the
// endpoint's native shape. Bodies without tools get the exact read/shell
// definitions; bodies that already carry tools keep them and gain only the
// missing fingerprint names appended (with conflicting caller tools
// aliased). Returns the rewritten body and the alias map for response
// restoration. A present-but-malformed tools value or unparseable JSON
// passes through untouched so upstream returns its own clear 4xx.
func ensureUpstreamTools(body []byte, endpoint string) ([]byte, map[string]string) {
	empty := make(map[string]string)
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body, empty
	}
	if t, ok := raw["tools"]; ok && t != nil {
		if _, ok := t.([]any); !ok {
			return body, empty
		}
	}
	aliasMap := applyFingerprintTools(raw, endpoint)
	out, err := json.Marshal(raw)
	if err != nil {
		return body, empty
	}
	action := "merged"
	if len(aliasMap) == 0 {
		action = "fingerprinted"
	}
	slog.Debug("opencode fingerprint tools "+action, "endpoint", endpoint, "aliases", len(aliasMap))
	return out, aliasMap
}

// restoreUpstreamBody restores caller tool spellings in a complete (non-SSE)
// upstream JSON body using the request's alias map.
func restoreUpstreamBody(body []byte, aliasToOriginal map[string]string) []byte {
	if len(aliasToOriginal) == 0 || len(body) == 0 {
		return body
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	restoreToolNamesInJSON(raw, aliasToOriginal)
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}

func restoreToolNamesInJSON(raw map[string]any, aliasToOriginal map[string]string) {
	// OpenAI chat: choices[].message.tool_calls[].function.name
	if choices, ok := raw["choices"].([]any); ok {
		for _, c := range choices {
			cm, _ := c.(map[string]any)
			if cm == nil {
				continue
			}
			for _, holder := range []string{"message", "delta"} {
				h, _ := cm[holder].(map[string]any)
				if h == nil {
					continue
				}
				tcs, _ := h["tool_calls"].([]any)
				for _, tc := range tcs {
					tcm, _ := tc.(map[string]any)
					if tcm == nil {
						continue
					}
					fn, _ := tcm["function"].(map[string]any)
					if fn == nil {
						continue
					}
					if name, _ := fn["name"].(string); name != "" {
						if orig, ok := aliasToOriginal[name]; ok {
							fn["name"] = orig
						}
					}
				}
			}
		}
	}
	// OpenAI Responses: output[].name (function_call)
	if output, ok := raw["output"].([]any); ok {
		for _, item := range output {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			if m["type"] == "function_call" {
				if name, _ := m["name"].(string); name != "" {
					if orig, ok := aliasToOriginal[name]; ok {
						m["name"] = orig
					}
				}
			}
		}
	}
	// Claude Messages: content[].name (tool_use)
	if content, ok := raw["content"].([]any); ok {
		for _, block := range content {
			m, _ := block.(map[string]any)
			if m == nil {
				continue
			}
			if m["type"] == "tool_use" {
				if name, _ := m["name"].(string); name != "" {
					if orig, ok := aliasToOriginal[name]; ok {
						m["name"] = orig
					}
				}
			}
		}
	}
}

// aliasRestoreReader streams an SSE body back while restoring aliased tool
// names. A trailing overlap is held back across Reads so a quoted name split
// across chunk boundaries is still rewritten; the remainder flushes on EOF.
type aliasRestoreReader struct {
	src     io.ReadCloser
	aliases map[string]string
	pending []byte
	overlap int
	done    bool
}

func wrapAliasRestoreStream(src io.ReadCloser, aliasToOriginal map[string]string) io.ReadCloser {
	if len(aliasToOriginal) == 0 {
		return src
	}
	maxLen := 0
	for alias := range aliasToOriginal {
		if len(alias)+2 > maxLen {
			maxLen = len(alias) + 2
		}
	}
	return &aliasRestoreReader{src: src, aliases: aliasToOriginal, overlap: maxLen + 8}
}

func (r *aliasRestoreReader) restoreChunk(chunk []byte) []byte {
	for alias, orig := range r.aliases {
		chunk = bytes.ReplaceAll(chunk, []byte(`"`+alias+`"`), []byte(`"`+orig+`"`))
	}
	return chunk
}

func (r *aliasRestoreReader) Read(p []byte) (int, error) {
	if r.done && len(r.pending) == 0 {
		return 0, io.EOF
	}
	buf := make([]byte, len(p)+r.overlap+4096)
	var data []byte
	if len(r.pending) > 0 {
		data = append(data, r.pending...)
		r.pending = nil
	}
	n, err := r.src.Read(buf)
	if n > 0 {
		data = append(data, buf[:n]...)
	}
	if err == io.EOF || (err == nil && n == 0) {
		// No progress; flush what remains.
		if len(data) == 0 {
			r.done = true
			return 0, io.EOF
		}
		if err == io.EOF {
			r.done = true
			out := r.restoreChunk(data)
			m := copy(p, out)
			if m < len(out) {
				r.pending = append([]byte(nil), out[m:]...)
				// Keep done=true so the tail drains then EOFs.
			}
			return m, nil
		}
	}
	if err != nil && err != io.EOF {
		if len(data) == 0 {
			return 0, err
		}
		// Emit what we have; surface the error next read.
		r.done = true
		out := r.restoreChunk(data)
		return copy(p, out), nil
	}
	if len(data) <= r.overlap {
		r.pending = data
		return r.Read(p)
	}
	emit := data[:len(data)-r.overlap]
	r.pending = append([]byte(nil), data[len(data)-r.overlap:]...)
	out := r.restoreChunk(emit)
	// restoreChunk never grows beyond a few bytes per replacement; if it
	// overflows p, stash the remainder.
	if len(out) > len(p) {
		r.pending = append(out[len(p):], r.pending...)
		return copy(p, out[:len(p)]), nil
	}
	return copy(p, out), nil
}

func (r *aliasRestoreReader) Close() error {
	if r.src == nil {
		return nil
	}
	return r.src.Close()
}
