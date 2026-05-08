package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
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

// runRequest carries the resolved-once request fields shared by every
// protocol version of the run handler.
type runRequest struct {
	project           *store.Project
	live              *store.Deployment
	command           string
	serviceName       string
	imageTag          string
	deploymentNetwork string
	extraParams       []string
}

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
//   - tty      (optional, v2 only) — when "1", the daemon allocates a
//     PTY for the container so interactive programs (vim, top, color
//     output) work. Defaults to off.
//
// The container is attached to the live deployment's network plus
// cobalt-main, mirroring the hook flow. extraRunParams from the
// cobaltfile are honored for `--add-host`-style flags.
//
// Subprotocol negotiation: the handler advertises both
// cobalt-run.v2 (kubectl-style, binary multiplexed) and cobalt-run.v1
// (legacy JSON). The client's first preference wins; we dispatch
// based on conn.Subprotocol().
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

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// v2 first so a client that speaks both wins v2.
		Subprotocols: []string{
			cobaltapi.RunSubprotocolV2,
			cobaltapi.RunSubprotocolV1,
		},
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

	req := runRequest{
		project:           project,
		live:              live,
		command:           cmd,
		serviceName:       serviceName,
		imageTag:          docker.InternalImageName(project.Name, imageName, live.Number),
		deploymentNetwork: docker.NetworkName(project.Name, live.Number),
		extraParams:       extraParams,
	}

	switch conn.Subprotocol() {
	case cobaltapi.RunSubprotocolV2:
		tty := r.URL.Query().Get("tty") == "1"
		h.Log.Info("run: v2 session", "project", project.Name, "service", serviceName, "tty", tty)
		h.runV2(r.Context(), conn, req, tty)
	default:
		h.Log.Info("run: v1 session (deprecated)", "project", project.Name, "service", serviceName)
		h.runV1(r.Context(), conn, req)
	}
}

// runV1 is the legacy JSON-text-frame protocol kept on the server side
// for old CLIs. New work goes into runV2; v1 will be removed one
// release after v2 ships in a stable CLI.
//
// The original handler used io.Pipe() for stdin, which deadlocked
// exec.Cmd.Wait — see plans/cobalt/cobalt-run-v2.md "deadlock disguised
// as a 60 s upper bound". We use os.Pipe() here so the file descriptor
// reaches the child as a real *os.File and exec.Cmd skips its internal
// io.Copy goroutine.
func (h *Handler) runV1(ctx context.Context, conn *websocket.Conn, req runRequest) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		h.Log.Error("run: stdin pipe", "error", err)
		return
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		h.Log.Error("run: stdout pipe", "error", err)
		return
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		h.Log.Error("run: stderr pipe", "error", err)
		return
	}

	var outWG sync.WaitGroup
	outWG.Add(2)
	go pumpToWS(&outWG, runCtx, conn, stdoutR, cobaltapi.RunFrameStdout)
	go pumpToWS(&outWG, runCtx, conn, stderrR, cobaltapi.RunFrameStderr)

	// Stdin pump — reads JSON frames off the WS, writes the bytes to
	// the os.Pipe writer. Closes the writer on WS error.
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

	runOpts := docker.RunOpts{
		ProjectID:        req.project.ID,
		ProjectName:      req.project.Name,
		ServiceName:      req.serviceName,
		DeploymentNumber: req.live.Number,
		ContainerName:    docker.RunContainerName(req.project.Name, time.Now().UnixNano()),
		Image:            req.imageTag,
		Command:          []string{"sh", "-c", req.command},
		Networks:         []string{req.deploymentNetwork, deploy.MainNetworkName},
		ExtraParams:      req.extraParams,
		Stdin:            stdinR,
		Stdout:           stdoutW,
		Stderr:           stderrW,
	}
	runErr := h.Docker.Run(runCtx, runOpts)

	// Closing the write halves makes the pumps see EOF and drain.
	// Closing stdinR releases the kernel handle the child held; with
	// os.Pipe() the child's fd is dup2'd, so this just frees ours.
	_ = stdoutW.Close()
	_ = stderrW.Close()
	_ = stdinR.Close()
	outWG.Wait()

	exitCode := 0
	if runErr != nil {
		exitCode = 1
		h.Log.Info("run: container exited non-zero",
			"project", req.project.Name, "service", req.serviceName, "error", runErr)
	}
	exitFrame, _ := json.Marshal(cobaltapi.RunFrame{
		Type: cobaltapi.RunFrameExit,
		Code: exitCode,
	})
	writeCtx, writeCancel := context.WithTimeout(context.Background(), runWriteTimeout)
	_ = conn.Write(writeCtx, websocket.MessageText, exitFrame)
	writeCancel()

	// Closing the WS unblocks the leaking stdin pump.
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// pumpToWS reads bytes from r and writes them as v1 RunFrames of the
// given type. Returns when r is closed (typical) or the context is
// canceled.
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
