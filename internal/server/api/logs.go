package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// followPollInterval is how often we check for new bytes appended to a
// deploy log file while the deployment is still in flight.
const followPollInterval = 200 * time.Millisecond

// heartbeatInterval is the SSE comment cadence — long enough to be
// invisible, short enough that reverse proxies (Caddy default ~120s)
// don't time us out.
const heartbeatInterval = 15 * time.Second

// DeploymentOutput implements GET /api/deployments/{id}/output. Streams
// the deployment's captured stdout/stderr as Server-Sent Events. If
// the deployment is still in flight, follows the file and emits new
// bytes as they're written. Closes when the deployment reaches a
// terminal state.
//
// Optional ?offset=N starts the read at byte N (clients implementing
// resume hold onto the last byte position they saw).
func (h *Handler) DeploymentOutput(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid deployment id")
		return
	}
	dep, err := h.DB.GetDeployment(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	project, err := h.DB.GetProjectByID(r.Context(), dep.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	offset, err := parseOffset(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.DataDir == "" {
		writeError(w, http.StatusInternalServerError, "daemon DataDir not configured")
		return
	}

	sse, err := newSSE(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	stop := make(chan struct{})
	defer close(stop)
	go sse.heartbeatLoop(stop, heartbeatInterval)

	path := deploy.DeployLogPath(h.DataDir, project.Name, dep.Number)
	if err := h.streamDeployFile(r.Context(), sse, path, offset, id); err != nil {
		// Don't writeError after sse.data has emitted bytes — the
		// response is already committed. Just log and return.
		h.Log.Warn("deploy output stream", "deployment_id", id, "error", err)
	}
}

// streamDeployFile reads path from offset onward and writes chunks to
// sse. While the deployment is non-terminal, EOF triggers a poll-and-
// retry loop; once terminal, EOF is the end.
func (h *Handler) streamDeployFile(
	ctx context.Context,
	sse *sseWriter,
	path string,
	offset int64,
	deploymentID int64,
) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		// No log file yet — wait briefly for the orchestrator to
		// create it, then bail if still missing.
		f, err = waitForFile(ctx, path, 2*time.Second)
		if err != nil {
			return nil // nothing to stream; treat as empty deploy
		}
	} else if err != nil {
		return err
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
	}

	buf := make([]byte, 4<<10)
	pos := offset
	for {
		n, err := f.Read(buf)
		if n > 0 {
			pos += int64(n)
			if perr := sse.data(strconv.FormatInt(pos, 10), string(buf[:n])); perr != nil {
				return perr
			}
		}
		switch {
		case err == nil:
			continue
		case errors.Is(err, io.EOF):
			// Either the deployment is done (close stream) or it's
			// still running (wait + retry).
			dep, derr := h.DB.GetDeployment(ctx, deploymentID)
			if derr != nil {
				return derr
			}
			if dep.Status.IsTerminal() {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(followPollInterval):
			}
		default:
			return err
		}
	}
}

// ProjectLogs implements GET /api/projects/{name}/logs. Streams logs
// from a long-running service of the project's most recent successful
// deployment. Optional ?service=NAME selects which service (defaults
// to "web").
//
// Implementation shells out to `docker service logs --follow` and
// pipes stdout into the SSE stream.
func (h *Handler) ProjectLogs(w http.ResponseWriter, r *http.Request) {
	project, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	live, err := h.DB.GetLastSuccessfulDeployment(r.Context(), project.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project has no successful deployment")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	serviceName := r.URL.Query().Get("service")
	if serviceName == "" {
		serviceName = "web"
	}
	// Reject unknown services upfront. Without this, the daemon
	// shells out to `docker service logs <full-name>`, docker emits
	// "no such task or service" into the SSE stream, and the CLI
	// thinks the stream succeeded — exit 0 hides a typo. Runs before
	// the Docker-client guard so "service typo" wins over "daemon
	// misconfigured" when both apply.
	if live.ResolvedCobaltfile != nil {
		if cf, err := cobaltfile.Parse([]byte(*live.ResolvedCobaltfile)); err == nil {
			if _, ok := cf.Services[serviceName]; !ok {
				writeError(w, http.StatusNotFound,
					"service "+serviceName+" not found in deployment #"+
						strconv.Itoa(live.Number)+" of project "+project.Name)
				return
			}
		}
	}
	if h.Docker == nil {
		writeError(w, http.StatusInternalServerError, "daemon Docker client not configured")
		return
	}
	fullName := docker.ServiceName(project.Name, live.Number, serviceName)

	sse, err := newSSE(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	stop := make(chan struct{})
	defer close(stop)
	go sse.heartbeatLoop(stop, heartbeatInterval)

	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	// Run docker in a goroutine so we can read its output here. ctx
	// cancellation propagates through to docker — when the client
	// disconnects, the docker subprocess gets killed.
	errCh := make(chan error, 1)
	go func() {
		defer pw.Close()
		errCh <- h.Docker.ServiceLogs(r.Context(), fullName, true, pw)
	}()

	buf := make([]byte, 4<<10)
	for {
		n, readErr := pr.Read(buf)
		if n > 0 {
			if perr := sse.data("", string(buf[:n])); perr != nil {
				return
			}
		}
		if readErr != nil {
			// Either docker exited (errCh has the reason) or the pipe
			// was closed by the deferred Close. Either way we're done.
			select {
			case e := <-errCh:
				if e != nil {
					h.Log.Warn("service logs", "service", fullName, "error", e)
				}
			default:
			}
			return
		}
	}
}

// parseOffset reads ?offset=N from the URL. Returns 0 when absent.
func parseOffset(r *http.Request) (int64, error) {
	s := r.URL.Query().Get("offset")
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errOffsetInvalid
	}
	return n, nil
}

var errOffsetInvalid = httpErr("offset must be a non-negative integer")

// waitForFile polls until path exists or budget elapses. Used by the
// deploy-output endpoint to handle the race between SSE connect and
// the orchestrator's first OpenDeployLog call.
func waitForFile(ctx context.Context, path string, budget time.Duration) (*os.File, error) {
	deadline := time.Now().Add(budget)
	for {
		f, err := os.Open(path)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
