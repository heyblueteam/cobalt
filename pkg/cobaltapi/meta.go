package cobaltapi

// MetaInfo is the response body of GET /api/meta/info — daemon-side
// facts useful during incident response or manual diagnostics.
type MetaInfo struct {
	Version    string `json:"version"`
	Hostname   string `json:"hostname,omitempty"`
	UptimeSecs int64  `json:"uptimeSecs"`
	StartedAt  int64  `json:"startedAt"`
}
