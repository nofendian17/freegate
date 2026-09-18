package claude

import (
	"regexp"
	"strings"
)

// SanitizeAssistantText removes agentic scaffolding that free-tier models
// (notably DeepSeek via the OpenCode free tier) echo into assistant text.
//
// Observed leakage (surfaced verbatim in Claude Code result text):
//   - <system-reminder>...</system-reminder> (sometimes mis-closed as </reminder>)
//   - <feature-flag>...</feature-flag> and child tags (<feature-flag-name> etc.)
//   - <EXTREMELY_IMPORTANT>...</EXTREMELY_IMPORTANT> / hyphen variant
//   - <SUBAGENT-STOP>...</SUBAGENT-STOP>
//   - DSML tool-call tags using ASCII or fullwidth pipes:
//     <|DSML|...>, <｜DSML｜...> (paired, standalone, or unterminated —
//     cf. vllm-project/vllm#54686 leak classes: runaway invoke names with
//     no closer, mis-spelled closers like </｜DSML｜>, misspelled openers
//     like <｜DSML｜tool-calls)
//   - stray <plan>/</plan> tags from malformed DSML blocks (same PR,
//     class 3); tags only, inner content is preserved
//   - orphan DSML-family closers without the DSML sigil, observed from
//     degenerate free-tier output: </tool_calls>, </tool_input_cp_reminder>,
//     and bare <invoke>/<parameter> forms; tags only, inner content kept
//   - git diff marker lines: "\ No newline at end of file"
//
// Only assistant *text* fields (content, reasoning_content, reasoning)
// should be passed here — never tool-call arguments, where stripping
// would corrupt JSON.
//
// NOTE: this operates on one complete text value. Streaming callers
// sanitize per delta, so a tag split across two SSE deltas is a
// best-effort miss; in practice upstream deltas carry whole words/tags.
//
// The function is idempotent and returns the input unchanged when no
// scaffolding is present, so it is safe on the hot path.
func SanitizeAssistantText(s string) string {
	if s == "" {
		return s
	}
	// Fast path: skip regex work when none of the markers are present.
	if !containsScaffoldMarker(s) {
		return s
	}
	orig := s
	// DSML paired blocks first (may contain angle brackets inside attrs).
	for _, re := range dsmlPairedRes {
		s = re.ReplaceAllString(s, "")
	}
	// Known paired scaffold tags. Looped twice to collapse nesting
	// (e.g. <feature-flag> wrapping <feature-flag-name>).
	for i := 0; i < 2; i++ {
		for _, re := range scaffoldPairedRes {
			s = re.ReplaceAllString(s, "")
		}
	}
	// Known mismatched close observed in the wild:
	// <system-reminder>...</reminder>
	s = systemReminderMismatchRe.ReplaceAllString(s, "")
	// Leftover standalone tags (unclosed, or close without open).
	for _, re := range scaffoldSingleRes {
		s = re.ReplaceAllString(s, "")
	}
	s = dsmlSingleRe.ReplaceAllString(s, "")
	s = planSingleRe.ReplaceAllString(s, "")
	// Orphan DSML-family tags without the sigil (degenerate model output
	// drops the <｜DSML｜> wrapper but keeps tag names). Standalone-only,
	// like <plan>: unlike scaffold pairs, inner content is preserved.
	s = orphanTagRe.ReplaceAllString(s, "")
	// Unterminated DSML opener (runaway invoke name with no closer and no
	// closing `>`, vllm#54686 class 1): strip from the opener to the end.
	// DSML markup is never legitimate in assistant prose — real tool calls
	// travel via tool_calls — so an opener that never terminates is leak
	// by definition.
	s = dsmlUnterminatedRe.ReplaceAllString(s, "")
	// Git diff marker lines.
	s = gitNoNewlineRe.ReplaceAllString(s, "")
	if s == orig {
		return s
	}
	s = multiBlankRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func containsScaffoldMarker(s string) bool {
	// Cheap substring pre-filter before regex. Covers ASCII and fullwidth
	// DSML pipes, all scaffold tag names, and the git marker.
	for _, m := range scaffoldMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	// Case-insensitive check for the uppercase variants without
	// allocating: only scan when '<' is present at all.
	if !strings.Contains(s, "<") && !strings.Contains(s, `\ No newline`) {
		return false
	}
	lower := strings.ToLower(s)
	for _, m := range scaffoldMarkersLower {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

var scaffoldMarkers = []string{
	"DSML",
	"dsml",
	"No newline at end of file",
	"feature-flag",
	"system-reminder",
	"reminder",
	"SUBAGENT",
	"subagent",
	"EXTREMELY",
	"extremely",
	"<plan",
	"plan>",
}

var scaffoldMarkersLower = []string{
	"dsml",
	"no newline at end of file",
	"feature-flag",
	"system-reminder",
	"subagent",
	"extremely",
	"<plan",
	"plan>",
	"tool_calls",
	"tool-calls",
	"tool_input",
	"invoke>",
	"parameter>",
}

// scaffoldTagNames are stripped as <name>...</name> pairs and as
// standalone tags. Underscore/hyphen variants included.
var scaffoldTagNames = []string{
	`system-reminder`,
	`reminder`,
	`feature-flag`,
	`feature-flag-name`,
	`feature-flag-enabled`,
	`feature-flag-description`,
	`extremely[-_]important`,
	`subagent[-_]stop`,
}

var scaffoldPairedRes []*regexp.Regexp
var scaffoldSingleRes []*regexp.Regexp
var dsmlPairedRes []*regexp.Regexp

var (
	// <system-reminder>...</reminder> mismatched close seen in the wild.
	systemReminderMismatchRe = regexp.MustCompile(`(?is)<\s*system-reminder\s*>.*?</\s*reminder\s*>`)
	// Standalone DSML tags: <|DSML|...>, <｜DSML｜...>, </...> variants.
	// Case-insensitive to match the pre-filter (observed tags are
	// uppercase, but lowercase must not pass through silently).
	dsmlSingleRe = regexp.MustCompile(`(?i)<\s*/?\s*[|｜]DSML[|｜][^>]*>`)
	// Unterminated DSML opener running to the end of the text (no `>`).
	// `[^>]` spans newlines, `$` anchors to the end of the text.
	dsmlUnterminatedRe = regexp.MustCompile(`(?i)<\s*[|｜]DSML[|｜][^>]*$`)
	// Stray <plan>/</plan> tags from malformed DSML blocks (vllm#54686
	// class 3). Standalone-only: unlike the scaffold pairs above, a
	// <plan>...</plan> block may be legitimate model structuring, so only
	// the tags are removed and inner content is preserved.
	planSingleRe = regexp.MustCompile(`(?i)<\s*/?\s*plan\s*/?\s*>`)
	// Orphan DSML-family tags without the sigil (degenerate free-tier
	// output emits bare </tool_calls>, </tool_input_cp_reminder>, etc.).
	// Standalone open/close only; inner content is preserved. An optional
	// leading `]]` (CDATA-residue glued to the tag, e.g. `]]</parameter>`)
	// is consumed with it; lone `]]` in prose is left alone.
	orphanTagRe = regexp.MustCompile(`(?i)(?:\]\])?<\s*/?\s*(tool_calls|tool-calls|toolcall|invoke|parameter|tool_input_cp_reminder)\s*/?\s*>`)
	// Git diff marker line.
	gitNoNewlineRe = regexp.MustCompile(`(?m)^\\ No newline at end of file\s*\r?$`)
	multiBlankRe   = regexp.MustCompile(`\n{3,}`)
)

func init() {
	for _, tag := range scaffoldTagNames {
		scaffoldPairedRes = append(scaffoldPairedRes,
			regexp.MustCompile(`(?is)<\s*`+tag+`\s*>.*?</\s*`+tag+`\s*>`))
		scaffoldSingleRes = append(scaffoldSingleRes,
			regexp.MustCompile(`(?i)<\s*/?\s*`+tag+`\s*/?\s*>`))
	}
	// DSML paired: ASCII pipe and fullwidth pipe (U+FF5C) variants,
	// plus mixed variants. Case-insensitive to match the pre-filter.
	for _, pipe := range []string{`\|`, `｜`} {
		dsmlPairedRes = append(dsmlPairedRes, regexp.MustCompile(
			`(?is)<\s*`+pipe+`DSML`+pipe+`[^>]*>.*?</\s*`+pipe+`DSML`+pipe+`[^>]*>`))
	}
	// Mixed-pipe paired (open ASCII, close fullwidth and vice versa).
	dsmlPairedRes = append(dsmlPairedRes, regexp.MustCompile(
		`(?is)<\s*[|｜]DSML[|｜][^>]*>.*?</\s*[|｜]DSML[|｜][^>]*>`))
}
