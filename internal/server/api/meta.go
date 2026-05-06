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
