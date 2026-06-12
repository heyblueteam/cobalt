package cobaltapi

import "time"

// ServerStats is the response from GET /api/server/stats — one snapshot of
// host- and container-level resource usage on the node the daemon runs on.
// With ?follow=1 the endpoint streams these as SSE events every ~2s.
//
// All sizes are bytes: the daemon parses docker's human-formatted strings
// once at the source so clients (including --json consumers) never re-parse.
type ServerStats struct {
	// Node is the daemon host's hostname. Single-node today; keyed so a
	// future multi-node fan-in can aggregate without a wire change.
	Node       string           `json:"node"`
	SampledAt  time.Time        `json:"sampledAt"`
	System     SystemStats      `json:"system"`
	Containers []ContainerStats `json:"containers"`
}

// SystemStats is host-level usage sampled from /proc and statfs.
type SystemStats struct {
	// CPUPercent is whole-host utilisation across all cores, 0–100.
	// Computed from the delta between consecutive samples, so the first
	// sample of a connection reports 0.
	CPUPercent     float64     `json:"cpuPercent"`
	CPUCount       int         `json:"cpuCount"`
	Load1          float64     `json:"load1"`
	Load5          float64     `json:"load5"`
	Load15         float64     `json:"load15"`
	MemUsedBytes   uint64      `json:"memUsedBytes"`
	MemTotalBytes  uint64      `json:"memTotalBytes"`
	SwapUsedBytes  uint64      `json:"swapUsedBytes"`
	SwapTotalBytes uint64      `json:"swapTotalBytes"`
	UptimeSeconds  int64       `json:"uptimeSeconds"`
	Disks          []DiskStats `json:"disks"`
}

// DiskStats is one filesystem's usage.
type DiskStats struct {
	Mount      string `json:"mount"`
	UsedBytes  uint64 `json:"usedBytes"`
	TotalBytes uint64 `json:"totalBytes"`
}

// ContainerStats is one container's usage as reported by `docker stats`,
// attributed back to the cobalt project/service that owns it.
type ContainerStats struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// Project and Service are empty for containers cobalt doesn't manage
	// (clients group those under "other"). Deployment is the cobalt
	// deployment number; Slot is the swarm task slot (1-based replica
	// index), 0 when unknown.
	Project    string `json:"project,omitempty"`
	Service    string `json:"service,omitempty"`
	Deployment int    `json:"deployment,omitempty"`
	Slot       int    `json:"slot,omitempty"`

	CPUPercent      float64 `json:"cpuPercent"`
	MemUsedBytes    uint64  `json:"memUsedBytes"`
	MemLimitBytes   uint64  `json:"memLimitBytes"`
	NetRxBytes      uint64  `json:"netRxBytes"`
	NetTxBytes      uint64  `json:"netTxBytes"`
	BlockReadBytes  uint64  `json:"blockReadBytes"`
	BlockWriteBytes uint64  `json:"blockWriteBytes"`
	PIDs            int     `json:"pids"`
}
