package domain

type TimeseriesEntry struct {
	Timestamp     int64          `json:"ts"`
	TotalRequests int            `json:"total_requests"`
	Errors        int            `json:"errors"`
	Retries       int            `json:"retries"`
	RateLimitHits int            `json:"rate_limit_hits"`
	PerUpstream   map[string]int `json:"per_upstream"`
}
