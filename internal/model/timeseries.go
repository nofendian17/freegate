package model

import "time"

// TimeseriesEntry is a snapshot of metrics counters at a point in time.
type TimeseriesEntry struct {
	Ts            time.Time      `json:"ts"`
	TotalRequests int64          `json:"total_requests"`
	Errors        int64          `json:"errors"`
	Retries       int64          `json:"retries"`
	RateLimitHits int64          `json:"rate_limit_hits"`
	PerUpstream   map[string]int `json:"per_upstream"`
}
