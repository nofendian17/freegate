package supervisor

// ServerInfo is one relay server offered by the supervisor's /servers
// endpoint (already filtered by the configured filters).
type ServerInfo struct {
	Hostname    string `json:"hostname"`
	IP          string `json:"ip"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Score       int    `json:"score"`
	Ping        string `json:"ping"`
}

// StatusInfo is the supervisor's current tunnel state (GET /status).
type StatusInfo struct {
	Connected   bool   `json:"connected"`
	Server      string `json:"server"`
	Country     string `json:"country"`
	IP          string `json:"ip"`
	ConnectedAt int64  `json:"connected_at"`
}

// PingResult is the outcome of a live connectivity check (POST /ping):
// DNS resolution, an HTTPS egress probe with latency, and the current
// tunnel state. Direct is filled in by the proxy side (the supervisor has
// no notion of the dialer route) so the dashboard can tell the user that
// the ping reflects the tunnel, not the active route.
type PingResult struct {
	Connected bool   `json:"connected"`
	Direct    bool   `json:"direct"`
	Server    string `json:"server"`
	Country   string `json:"country"`
	IP        string `json:"ip"`

	DNSOK    bool   `json:"dns_ok"`
	DNSMS    int64  `json:"dns_ms"`
	DNSError string `json:"dns_error,omitempty"`

	EgressOK  bool   `json:"egress_ok"`
	EgressIP  string `json:"egress_ip,omitempty"`
	HTTPMS    int64  `json:"http_ms"`
	HTTPCode  int    `json:"http_code"`
	EgressErr string `json:"egress_error,omitempty"`
}
