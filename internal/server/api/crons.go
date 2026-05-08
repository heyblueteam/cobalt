package api

import (
	"net/http"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ListProjectCrons implements GET /api/projects/{name}/crons. Returns
// the cron services registered for the project, with the scheduler's
// next-fire time per service. Empty list when nothing is registered.
func (h *Handler) ListProjectCrons(w http.ResponseWriter, r *http.Request) {
	project, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	if h.CronManager == nil {
		writeJSON(w, []cobaltapi.ProjectCron{})
		return
	}
	views := h.CronManager.ListForProject(project.Name)
	out := make([]cobaltapi.ProjectCron, 0, len(views))
	for _, v := range views {
		entry := cobaltapi.ProjectCron{
			Service:          v.ServiceName,
			Schedule:         v.Schedule,
			Command:          v.Command,
			DeploymentNumber: v.DeploymentNumber,
		}
		if !v.NextFireAt.IsZero() {
			entry.NextFireAt = v.NextFireAt.Unix()
		}
		out = append(out, entry)
	}
	writeJSON(w, out)
}
