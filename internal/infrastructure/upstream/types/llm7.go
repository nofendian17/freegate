package types

// LLM7ModelList is the /v1/models response from api.llm7.io.
type LLM7ModelList struct {
	Object string      `json:"object"`
	Data   []LLM7Model `json:"data"`
}

// LLM7Model mirrors the fields freegate needs. usage_based_only=false (or
// tier "turbo" without a per-token charge) marks an anonymously usable model.
// Only the filter-relevant fields are decoded; extra catalog fields are ignored.
type LLM7Model struct {
	ID             string `json:"id"`
	UsageBasedOnly *bool  `json:"usage_based_only,omitempty"`
	Tier           string `json:"tier,omitempty"`
}
