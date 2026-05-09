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

// MetaUpgrade is the deprecated alias for ServerUpgrade — kept so
// older CLIs can keep upgrading until they roll forward. Just calls
// the new handler. Will be removed one minor release after every
// supported install has the new CLI.
func (h *Handler) MetaUpgrade(w http.ResponseWriter, r *http.Request) {
	h.ServerUpgrade(w, r)
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
