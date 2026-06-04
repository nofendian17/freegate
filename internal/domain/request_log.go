package domain

// RequestLogEntry describes a single proxied chat request, used by the
// dashboard's "recent requests" view and the recorder ring buffer.
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

// RequestLogger is the port through which the application layer
// reports a finished request to a recorder. It is a function type so
// the domain doesn't need to know who is logging.
type RequestLogger func(RequestLogEntry)
