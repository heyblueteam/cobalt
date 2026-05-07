package api

import (
	"net/http"
	"time"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// GetMetaInfo implements GET /api/meta/info — daemon version, the
// hostname it was started with, and uptime.
func (h *Handler) GetMetaInfo(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	uptime := int64(0)
	startedAt := int64(0)
	if !h.StartedAt.IsZero() {
		uptime = int64(now.Sub(h.StartedAt).Seconds())
		startedAt = h.StartedAt.Unix()
	}
	hostname := h.PublicHost
	writeJSON(w, cobaltapi.MetaInfo{
		Version:    h.Version,
		Hostname:   hostname,
		UptimeSecs: uptime,
		StartedAt:  startedAt,
	})
}

// MetaUpgrade implements POST /api/meta/upgrade. For v1 this is a stub —
// the daemon cannot safely self-upgrade while running. Manual upgrade via
// `docker service update --image ghcr.io/heyblueteam/cobalt:<tag> cobalt` is
// required. Returns 501 with a message directing users to the manual process.
func (h *Handler) MetaUpgrade(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented,
		"manual upgrade required: docker service update --image <image> cobalt")
}

// MetaHost implements POST /api/meta/host. Updates the daemon's public
// hostname and propagates the change to Caddy's route matcher.
func (h *Handler) MetaHost(w http.ResponseWriter, r *http.Request) {
	var req cobaltapi.MetaHostRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	if req.Host == "" {
		writeError(w, http.StatusBadRequest, "host is required")
		return
	}
	h.PublicHost = req.Host
	if h.Caddy != nil {
		if err := h.Caddy.UpdateDaemonHost(r.Context(), req.Host); err != nil {
			h.Log.Warn("api: meta host: caddy update failed", "host", req.Host, "error", err)
		}
	}
	h.GetMetaInfo(w, r)
}
