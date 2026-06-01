package model

import "time"

// RequestLogEntry describes a single proxied request for observability.
type RequestLogEntry struct {
	Ts         time.Time `json:"ts"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Model      string    `json:"model"`
	Upstream   string    `json:"upstream"`
	Status     int       `json:"status"`
	DurationMs int64     `json:"duration_ms"`
	IP         string    `json:"ip"`
	Error      string    `json:"error,omitempty"`
}

// RequestLogger receives a notification when a request completes.
// Pass nil to disable logging.
type RequestLogger func(RequestLogEntry)
