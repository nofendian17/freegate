package domain

type RequestLogEntry struct {
	Time     string `json:"time"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Status   int    `json:"status"`
	Duration int64  `json:"duration_ms"`
	Tokens   int    `json:"tokens"`
	IP       string `json:"ip"`
	Error    string `json:"error,omitempty"`
}

type RequestLogger func(RequestLogEntry)
