package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// runWriteTimeout caps any single WS write (stdout chunks, exit frames).
// We don't want a slow client to indefinitely back up our docker copy.
const runWriteTimeout = 10 * time.Second

// Run implements GET /api/projects/{name}/run as a WebSocket endpoint.
// The CLI connects, the daemon starts a one-shot container against the
// last successful deployment's image, pipes stdin/stdout/stderr through
// the WS, and sends an exit frame when the container terminates.
//
// Query parameters:
//   - command  (required) — the shell command to execute inside the
//     container. Wrapped in `sh -c "..."` so users can compose pipes.
//   - service  (optional) — which service's image to use; defaults to
//     "web". The service must exist in the live deployment's cobaltfile.
//
// The container is attached to the live deployment's network plus
// cobalt-main, mirroring the hook flow. extraRunParams from the
// cobaltfile (PR #92 from upstream) are honored for `--add-host`-style
// flags so commands like `npx prisma migrate deploy` can reach
// host.docker.internal.
func (h *Handler) Run(w http.ResponseWriter, r *http.Request) {
	project, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	if h.Docker == nil {
		writeError(w, http.StatusInternalServerError, "daemon Docker client not configured")
		return
	}

	cmd := r.URL.Query().Get("command")
	if cmd == "" {
		writeError(w, http.StatusBadRequest, "command query parameter required")
		return
	}
	serviceName := r.URL.Query().Get("service")
	if serviceName == "" {
		serviceName = "web"
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

	// Resolve the service's image + extra run params from the live
	// deployment's cobaltfile. Falls back to "default" image if the
	// service isn't found or the cobaltfile is missing.
	imageName, extraParams := resolveRunImage(live, serviceName)
	imageTag := docker.InternalImageName(project.Name, imageName, live.Number)
	deploymentNetwork := docker.NetworkName(project.Name, live.Number)

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{cobaltapi.RunSubprotocol},
		// Don't enforce origin — this is an API endpoint consumed by
		// the cobalt CLI, not a browser. The bearer auth on the
		// upgrade request is the access control.
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept already wrote a status if it failed.
		return
	}
	defer conn.CloseNow()

	// runCtx is the scope of this run; canceling tears down docker +
	// goroutines. Either WS disconnect or container exit triggers it.
	runCtx, cancel := context.WithCancel(r.Context())
	defer cancel()

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	// outWG covers ONLY the output pumps (stdout/stderr → WS). The
	// stdin pump (WS → stdin pipe) blocks on conn.Read indefinitely
	// and would deadlock the handler if we waited on it; we let it
	// leak briefly until our final Close terminates the WS.
	var outWG sync.WaitGroup
	outWG.Add(2)
	go pumpToWS(&outWG, runCtx, conn, stdoutR, cobaltapi.RunFrameStdout)
	go pumpToWS(&outWG, runCtx, conn, stderrR, cobaltapi.RunFrameStderr)

	// Stdin pump (fire-and-forget). When runCtx is canceled at the end
	// of the handler, conn.Read returns and the goroutine exits.
	go func() {
		defer stdinW.Close()
		for {
			_, data, err := conn.Read(runCtx)
			if err != nil {
				return
			}
			var frame cobaltapi.RunFrame
			if err := json.Unmarshal(data, &frame); err != nil {
				continue
			}
			if frame.Type == cobaltapi.RunFrameStdin {
				if _, err := stdinW.Write([]byte(frame.Data)); err != nil {
					return
				}
			}
		}
	}()

	// Run the container. docker.Run blocks until the container exits
	// or ctx is canceled.
	runOpts := docker.RunOpts{
		ProjectID:        project.ID,
		ProjectName:      project.Name,
		ServiceName:      serviceName,
		DeploymentNumber: live.Number,
		ContainerName:    docker.RunContainerName(project.Name, time.Now().UnixNano()),
		Image:            imageTag,
		Command:          []string{"sh", "-c", cmd},
		Networks:         []string{deploymentNetwork, deploy.MainNetworkName},
		ExtraParams:      extraParams,
		Stdin:            stdinR,
		Stdout:           stdoutW,
		Stderr:           stderrW,
	}
	runErr := h.Docker.Run(runCtx, runOpts)

	// Closing the write halves makes the pumps see EOF and drain.
	_ = stdoutW.Close()
	_ = stderrW.Close()
	_ = stdinR.Close()
	outWG.Wait()

	exitCode := 0
	if runErr != nil {
		exitCode = 1
		h.Log.Info("run: container exited non-zero",
			"project", project.Name, "service", serviceName, "error", runErr)
	}
	exitFrame, _ := json.Marshal(cobaltapi.RunFrame{
		Type: cobaltapi.RunFrameExit,
		Code: exitCode,
	})
	writeCtx, writeCancel := context.WithTimeout(context.Background(), runWriteTimeout)
	_ = conn.Write(writeCtx, websocket.MessageText, exitFrame)
	writeCancel()

	// Now close the WS — this will also unblock the leaking stdin pump.
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// pumpToWS reads bytes from r and writes them as RunFrames of the given
// type. Returns when r is closed (typical) or the context is canceled.
func pumpToWS(wg *sync.WaitGroup, ctx context.Context, conn *websocket.Conn, r io.Reader, frameType string) {
	defer wg.Done()
	buf := make([]byte, 4<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			payload, mErr := json.Marshal(cobaltapi.RunFrame{
				Type: frameType,
				Data: string(buf[:n]),
			})
			if mErr != nil {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, runWriteTimeout)
			wErr := conn.Write(writeCtx, websocket.MessageText, payload)
			cancel()
			if wErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// resolveRunImage looks up the service in the live deployment's stored
// cobaltfile and returns its image name + extra run params. Falls back
// to "default" / no params if the cobaltfile isn't usable.
func resolveRunImage(live *store.Deployment, serviceName string) (image string, extraParams []string) {
	image = "default"
	if live.ResolvedCobaltfile == nil {
		return image, nil
	}
	cf, err := cobaltfile.Parse([]byte(*live.ResolvedCobaltfile))
	if err != nil {
		return image, nil
	}
	svc, ok := cf.Services[serviceName]
	if !ok {
		return image, nil
	}
	if svc.Image != "" {
		image = svc.Image
	}
	extraParams = docker.SplitParams(svc.ExtraRunParams)
	return image, extraParams
}

// We use a strconv reference here so static analyzers don't drop the
// import after future refactors strip the only call site. (cheap.)
var _ = strconv.Itoa
