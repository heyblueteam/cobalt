package cobaltapi

type MetaInfo struct {
	Version    string `json:"version"`
	Hostname   string `json:"hostname,omitempty"`
	UptimeSecs int64  `json:"uptimeSecs"`
	StartedAt  int64  `json:"startedAt"`
}

type MetaUpgradeRequest struct {
	Image    string `json:"image,omitempty"`
	DontPull bool   `json:"dontPull,omitempty"`
}

type MetaHostRequest struct {
	Host string `json:"host"`
}
