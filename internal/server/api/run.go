package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/middleware"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// runWriteTimeout caps any single WS write (stdout chunks, exit frames).
// We don't want a slow client to indefinitely back up our docker copy.
const runWriteTimeout = 10 * time.Second

// runMaxLifetime hard-caps any single cobalt run session. Forgotten
// interactive shells, runaway loops, etc. — none of them get to hold a
// container open indefinitely. 1 h matches the rough upper bound of
// "interactive ad-hoc usage"; longer-running workloads belong in a
// project service, not in cobalt run.
//
// Stored as an atomic so tests can shorten the cap to verify cancel
// behavior without sleeping for hours — and without racing the
// http-handler goroutine that reads it on every request.
var runMaxLifetime atomic.Int64

func init() {
	runMaxLifetime.Store(int64(1 * time.Hour))
}

// runHeartbeatInterval is the cadence of WS-protocol ping frames the
// daemon sends each cobalt-run client. Two purposes:
//
//   - Defeats idle-stream timeouts in any reverse proxy in front of the
//     daemon (Caddy's HTTP/2 server, Cloudflare, ALB, etc.) that close
//     "no traffic in either direction" connections.
//   - Detects a half-open peer (laptop closed mid-session, NAT eviction)
//     within ~30 s instead of waiting for the OS to notice on a write
//     attempt that may never come.
const runHeartbeatInterval = 30 * time.Second

// newRunLifecycle wraps the request context with the cobalt run
// session lifecycle: a 1 h hard cap (runMaxLifetime) and a 30 s WS
// heartbeat goroutine (runHeartbeatInterval). The returned cancel
// tears everything down — calling it stops the heartbeat goroutine,
// cancels in-flight docker work, and lets the handler exit cleanly.
//
// On heartbeat failure (peer gone, broken pipe), runCtx cancels
// automatically; the handler then sees its docker.Run returning
// because exec.Cmd cancellation kills the process.
func newRunLifecycle(parent context.Context, conn *websocket.Conn) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(runMaxLifetime.Load()))
	go func() {
		t := time.NewTicker(runHeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pingCtx, pingCancel := context.WithTimeout(ctx, runWriteTimeout)
				err := conn.Ping(pingCtx)
				pingCancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

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
	volumes           []docker.ServiceVolume
	// envVars is the merged env passed to the one-shot container: the
	// project's full env (cobalt env list) overlaid by the synthetic
	// COBALT_* context vars. Synthetic vars win on collision so the
	// runtime identity is always daemon-authoritative.
	envVars map[string]string
}

// cobaltSyntheticEnv builds the daemon-authoritative COBALT_* context
// vars injected into every one-shot run container. These identify the
// project / service / deployment the command is executing against;
// apps that branch worker-vs-web behavior on a single image can read
// COBALT_SERVICE_NAME instead of duplicating images.
//
// Optional vars are omitted (not set to "") when their source is empty
// so shell checks like `if [ -n "$COBALT_HOST" ]` work as expected.
func cobaltSyntheticEnv(project *store.Project, serviceName string, live *store.Deployment, publicHost string) map[string]string {
	out := map[string]string{
		"COBALT_PROJECT_NAME":      project.Name,
		"COBALT_SERVICE_NAME":      serviceName,
		"COBALT_DEPLOYMENT_NUMBER": strconv.Itoa(live.Number),
	}
	if publicHost != "" {
		out["COBALT_HOST"] = publicHost
	}
	if live.CommitSHA != nil && *live.CommitSHA != "" {
		out["COBALT_COMMIT"] = *live.CommitSHA
	}
	return out
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

	// Resolve the service's image + extra run params + volumes from
	// the live deployment's cobaltfile. Falls back to "default" image
	// if the service isn't found or the cobaltfile is missing.
	imageName, extraParams, volumes := resolveRunImage(live, project.ID, serviceName)

	// Load the project's env (everything in `cobalt env`) and overlay
	// the synthetic COBALT_* context vars. We soft-fail on store error
	// because `cobalt run` is interactive — an operator should still be
	// able to e.g. inspect a container when rqlite is having a bad
	// time. Deploys hard-fail here; runs don't.
	projectEnv, envErr := h.DB.EnvVarMap(r.Context(), project.ID)
	if envErr != nil {
		h.Log.Warn("run: env load failed; proceeding with synthetic only", "error", envErr)
		projectEnv = map[string]string{}
	}
	runEnv := make(map[string]string, len(projectEnv)+5)
	maps.Copy(runEnv, projectEnv)
	maps.Copy(runEnv, cobaltSyntheticEnv(project, serviceName, live, h.PublicHost)) // synthetic wins on collision

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
		volumes:           volumes,
		envVars:           runEnv,
	}

	// Audit row at handler entry. The exit code goes in once the
	// session ends. apikeyID is best-effort: 0 when the auth context
	// hasn't propagated (tests, etc.).
	tty := false
	if conn.Subprotocol() == cobaltapi.RunSubprotocolV2 {
		tty = r.URL.Query().Get("tty") == "1"
	}
	apikeyID := middleware.APIKeyIDFrom(r.Context())
	runID, runIDErr := h.DB.CreateCommandRun(r.Context(), project.ID, apikeyID, serviceName, cmd, tty)
	if runIDErr != nil {
		h.Log.Warn("run: audit insert failed", "error", runIDErr)
	}

	switch conn.Subprotocol() {
	case cobaltapi.RunSubprotocolV2:
		h.Log.Info("run: v2 session", "project", project.Name, "service", serviceName, "tty", tty, "audit_id", runID)
		exitCode := h.runV2(r.Context(), conn, req, tty)
		h.finalizeCommandRun(runID, exitCode)
	default:
		h.Log.Info("run: v1 session (deprecated)", "project", project.Name, "service", serviceName, "audit_id", runID)
		exitCode := h.runV1(r.Context(), conn, req)
		h.finalizeCommandRun(runID, exitCode)
	}
}

// finalizeCommandRun marks the audit row finished. Best-effort — log
// and move on if rqlite is grumpy. Uses a fresh context with timeout
// since the request context is typically already canceled by now.
func (h *Handler) finalizeCommandRun(runID int64, exitCode int) {
	if runID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.DB.FinishCommandRun(ctx, runID, exitCode); err != nil {
		h.Log.Warn("run: audit finalize failed", "audit_id", runID, "error", err)
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
func (h *Handler) runV1(ctx context.Context, conn *websocket.Conn, req runRequest) int {
	runCtx, cancel := newRunLifecycle(ctx, conn)
	defer cancel()

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		h.Log.Error("run: stdin pipe", "error", err)
		return -1
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		h.Log.Error("run: stdout pipe", "error", err)
		return -1
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		h.Log.Error("run: stderr pipe", "error", err)
		return -1
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
		EnvVars:          req.envVars,
		Networks:         docker.OneShotNetworks(deploy.MainNetworkName, req.deploymentNetwork),
		Volumes:          req.volumes,
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

	exitCode := docker.ExitCode(runErr)
	if runErr != nil {
		h.Log.Info("run: container exited non-zero",
			"project", req.project.Name, "service", req.serviceName,
			"exit_code", exitCode, "error", runErr)
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
	return exitCode
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
// cobaltfile and returns its image name + extra run params + the named
// docker volumes the live service has mounted. Falls back to "default" /
// no params / no volumes if the cobaltfile isn't usable.
//
// Mounting the same volumes the live service uses is what makes
// `cobalt run` actually behave like an exec into the project's
// environment — `ls /var/lib/postgres` sees the database files,
// inspecting upload dirs sees real uploads, etc.
func resolveRunImage(live *store.Deployment, projectID int64, serviceName string) (image string, extraParams []string, volumes []docker.ServiceVolume) {
	image = "default"
	if live.ResolvedCobaltfile == nil {
		return image, nil, nil
	}
	cf, err := cobaltfile.Parse([]byte(*live.ResolvedCobaltfile))
	if err != nil {
		return image, nil, nil
	}
	svc, ok := cf.Services[serviceName]
	if !ok {
		return image, nil, nil
	}
	if svc.Image != "" {
		image = svc.Image
	}
	extraParams = docker.SplitParams(svc.ExtraRunParams)
	for _, v := range svc.Volumes {
		volumes = append(volumes, docker.ServiceVolume{
			VolumeName:      docker.VolumeName(projectID, v.Name),
			DestinationPath: v.DestinationPath,
		})
	}
	return image, extraParams, volumes
}
