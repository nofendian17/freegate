package domain

import "time"

// TimeseriesEntry is a snapshot of metrics counters at a point in time,
// sampled for the dashboard's line chart.
type TimeseriesEntry struct {
	Ts            time.Time      `json:"ts"`
	TotalRequests int64          `json:"total_requests"`
	Errors        int64          `json:"errors"`
	InputTokens   int64          `json:"input_tokens"`
	OutputTokens  int64          `json:"output_tokens"`
	PerUpstream   map[string]int `json:"per_upstream"`
}
