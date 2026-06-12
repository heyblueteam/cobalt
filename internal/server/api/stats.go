package api

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// SystemSampler provides host-level statistics — a *sysstats.Session in
// production, a fake in tests. CPU utilisation is a delta between a
// sampler's consecutive Sample calls, so each request takes a fresh one
// from Handler.Sys; sharing a sampler across requests would interleave
// their measurement windows and degrade the CPU number to noise.
type SystemSampler interface {
	Sample() (cobaltapi.SystemStats, error)
}

// DefaultStatsInterval is the stats?follow=1 snapshot cadence: docker
// stats refreshes roughly every second, so 2s guarantees fresh container
// numbers in every snapshot without hammering `docker ps`.
const DefaultStatsInterval = 2 * time.Second

// ServerStats implements GET /api/server/stats — one snapshot of host +
// per-container usage, or an SSE stream of them with ?follow=1.
func (h *Handler) ServerStats(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil || h.Sys == nil {
		writeError(w, http.StatusInternalServerError, "stats sampling not configured")
		return
	}
	if r.URL.Query().Get("follow") != "" {
		h.serverStatsFollow(w, r)
		return
	}
	ctx := r.Context()
	sys := h.Sys()
	// Prime the CPU delta so docker's ~2s sampling window doubles as the
	// measurement window for the host CPU number. The reading itself is
	// discarded — only the stored counters matter.
	_, _ = sys.Sample()
	usages, err := h.Docker.StatsOnce(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "docker stats: "+err.Error())
		return
	}
	snap, err := h.assembleStats(ctx, sys, usages)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, snap)
}

func (h *Handler) serverStatsFollow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sse, err := newSSE(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	interval := h.StatsInterval
	if interval == 0 {
		interval = DefaultStatsInterval
	}
	stream := h.Docker.StatsStream(ctx)
	sys := h.Sys()
	_, _ = sys.Sample() // prime the CPU delta; reading discarded

	emit := func() error {
		snap, err := h.assembleStats(ctx, sys, stream.Snapshot())
		if err != nil {
			return err
		}
		b, err := jsonMarshal(snap)
		if err != nil {
			return err
		}
		return sse.data("", string(b))
	}

	// First snapshot immediately — a dashboard should paint at open, not
	// one interval later. Its container list may still be empty (docker
	// stats hasn't emitted yet); the next tick fills it.
	if err := emit(); err != nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stream.Done():
			// docker stats died under us (docker restart, error). Emit
			// what we have — best effort, the stream is closing either
			// way — and let the client reconnect with backoff.
			_ = emit()
			return
		case <-ticker.C:
			if err := emit(); err != nil {
				return
			}
		}
	}
}

// assembleStats joins host stats, container usage, and ownership into one
// wire snapshot.
func (h *Handler) assembleStats(ctx context.Context, sampler SystemSampler, usages []docker.ContainerUsage) (cobaltapi.ServerStats, error) {
	sys, err := sampler.Sample()
	if err != nil {
		return cobaltapi.ServerStats{}, err
	}
	projects, err := h.DB.ListProjects(ctx)
	if err != nil {
		return cobaltapi.ServerStats{}, err
	}
	names := make([]string, len(projects))
	for i, p := range projects {
		names[i] = p.Name
	}
	owners, err := h.Docker.ContainerOwners(ctx, names)
	if err != nil {
		// Stats without attribution beat no stats — every container just
		// lands in the client's "other" group until ps recovers.
		owners = nil
	}
	snap := cobaltapi.ServerStats{
		Node:       hostname(h.PublicHost),
		SampledAt:  time.Now().UTC(),
		System:     sys,
		Containers: make([]cobaltapi.ContainerStats, 0, len(usages)),
	}
	for _, u := range usages {
		o := owners[u.ID]
		snap.Containers = append(snap.Containers, cobaltapi.ContainerStats{
			ID:              u.ID,
			Name:            u.Name,
			Project:         o.Project,
			Service:         o.Service,
			Deployment:      o.Deployment,
			Slot:            o.Slot,
			CPUPercent:      u.CPUPercent,
			MemUsedBytes:    u.MemUsedBytes,
			MemLimitBytes:   u.MemLimitBytes,
			NetRxBytes:      u.NetRxBytes,
			NetTxBytes:      u.NetTxBytes,
			BlockReadBytes:  u.BlockReadBytes,
			BlockWriteBytes: u.BlockWriteBytes,
			PIDs:            u.PIDs,
		})
	}
	return snap, nil
}

// hostname is the OS hostname, falling back to the configured public
// host — Node must never be empty, multi-node fan-in keys on it.
func hostname(fallback string) string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	if fallback != "" {
		return fallback
	}
	return "unknown"
}

// statsMounts is the default disk set: the root filesystem plus the
// daemon's data dir (build/image storage). On most installs that's the
// same filesystem — the sampler dedupes identical readings.
func statsMounts(dataDir string) []string {
	if dataDir == "" {
		return []string{"/"}
	}
	return []string{"/", dataDir}
}
