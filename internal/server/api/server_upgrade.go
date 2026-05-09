package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ServerUpgrade implements POST /api/server/upgrade. Validates the
// request, records an upgrade row, spawns a detached helper container
// that performs the swap (so the helper outlives this daemon process),
// and returns 202 with the upgrade id for the CLI to follow.
//
// Single-flight: refuses with 409 when an upgrade is already in
// flight. Lets the operator follow that one rather than racing two
// helper containers against the same compose project.
func (h *Handler) ServerUpgrade(w http.ResponseWriter, r *http.Request) {
	var req cobaltapi.ServerUpgradeRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	if req.Image == "" {
		writeError(w, http.StatusBadRequest, "image is required")
		return
	}
	if h.Docker == nil {
		writeError(w, http.StatusInternalServerError, "daemon Docker client not configured")
		return
	}
	if h.DataDir == "" {
		writeError(w, http.StatusInternalServerError, "daemon DataDir not configured")
		return
	}
	if h.PublicHost == "" {
		writeError(w, http.StatusInternalServerError,
			"daemon PublicHost not configured — required for the post-upgrade health probe")
		return
	}

	// 409 if there's a recent upgrade still running. A row that's
	// been running for more than 10 min is presumed dead — the helper
	// crashed or the daemon was killed mid-upgrade — so we mark it
	// failed and let the new request proceed instead of blocking
	// forever. SweepStaleUpgrades handles this on boot; this inline
	// check covers the much-shorter staleness window between boots.
	if existing, err := h.DB.LatestRunningUpgrade(r.Context()); err == nil && existing != nil {
		const maxRunning = 10 * time.Minute
		ageSecs := time.Now().Unix() - existing.StartedAt
		if ageSecs > int64(maxRunning.Seconds()) {
			_ = h.DB.SetUpgradeStatus(r.Context(), existing.ID,
				store.UpgradeStatusFailed,
				"upgrade row was running for more than 10 min — presumed dead, cleared by next request")
			h.Log.Warn("cleared stale running upgrade",
				"upgrade_id", existing.ID, "age_secs", ageSecs)
		} else {
			writeError(w, http.StatusConflict,
				"upgrade "+existing.ID+" already running — follow with /api/server/upgrade/"+existing.ID+"/output")
			return
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.Log.Error("api: check running upgrade", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Determine rollback image — what's running NOW. The helper
	// inspects the container itself, but giving the daemon's view as
	// a hint helps the helper bail early if Docker disagrees.
	rollbackImage, err := currentDaemonImage(r.Context(), h.Docker)
	if err != nil {
		h.Log.Warn("api: read current daemon image (continuing)", "error", err)
	}

	id := newUpgradeID()
	logPath := upgradeLogPath(h.DataDir, id)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "create log dir: "+err.Error())
		return
	}
	// Touch the log file so the SSE follow can open it without racing
	// the helper's first write.
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Close()
	}

	upgrade := store.Upgrade{
		ID:          id,
		TargetImage: req.Image,
		FromVersion: h.Version,
		Status:      store.UpgradeStatusRunning,
		LogPath:     logPath,
		StartedAt:   time.Now().Unix(),
	}
	if v := imageTagAsVersion(req.Image); v != "" {
		upgrade.TargetVersion = v
	}
	if err := h.DB.CreateUpgrade(r.Context(), upgrade); err != nil {
		writeError(w, http.StatusInternalServerError, "record upgrade: "+err.Error())
		return
	}

	// Spawn the detached helper container. The helper image == the
	// target image (we trust the new release to know how to upgrade
	// itself). If that image isn't pullable, the helper crash will be
	// surfaced via the SSE stream timing out without ever reaching a
	// terminal status; SweepStaleUpgrades catches that on next boot.
	helperName := "cobalt-upgrade-" + id
	helperCmd := []string{
		"server-upgrade-helper",
		"--upgrade-id", id,
		"--target-image", req.Image,
		"--log-path", "/cobalt/data/upgrades/" + id + ".log",
		"--rqlite-url", "http://rqlite:4001",
		"--service-name", swarmServiceName(),
		"--public-host", h.PublicHost,
	}
	if rollbackImage != "" {
		helperCmd = append(helperCmd, "--rollback-image", rollbackImage)
	}
	if !req.Pull {
		helperCmd = append(helperCmd, "--no-pull")
	}

	// The helper needs to write its log to the SAME volume the daemon
	// reads from for SSE streaming. In Swarm mode the volume is named
	// `<stack>_<compose-volume>` (e.g. cobalt_cobalt-data) — the bare
	// `cobalt-data` would silently create a new empty volume the
	// daemon doesn't see, and the SSE follow times out reading an
	// empty file. Discover the actual source by inspecting our own
	// container's mounts; fall back to the Swarm default.
	dataVolume := dataVolumeName()
	// Override entrypoint to skip the image's default `cobalt server`
	// — the helper invokes the `server-upgrade-helper` subcommand
	// directly, which would otherwise be interpreted as an extra arg
	// to `server`.
	_, runErr := h.Docker.RunDetached(r.Context(), docker.DetachedRunOpts{
		Name:       helperName,
		Image:      req.Image,
		Entrypoint: "/usr/local/bin/cobalt",
		BindMounts: []string{
			"/var/run/docker.sock:/var/run/docker.sock",
			dataVolume + ":/cobalt/data",
		},
		EnvVars: map[string]string{
			"COBALT_DATA_DIR": "/cobalt/data",
		},
		Command: helperCmd,
	})
	if runErr != nil {
		// Helper failed to even start — mark the row failed so the
		// next request isn't blocked by a phantom running upgrade.
		_ = h.DB.SetUpgradeStatus(context.Background(), id,
			store.UpgradeStatusFailed,
			"failed to spawn helper container: "+runErr.Error())
		writeError(w, http.StatusInternalServerError,
			"spawn helper: "+runErr.Error())
		return
	}

	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, cobaltapi.ServerUpgrade{
		ID:            upgrade.ID,
		TargetImage:   upgrade.TargetImage,
		TargetVersion: upgrade.TargetVersion,
		FromVersion:   upgrade.FromVersion,
		Status:        upgrade.Status,
		StartedAt:     upgrade.StartedAt,
	})
}

// GetServerUpgrade implements GET /api/server/upgrade/{id}. Polled by
// the CLI to check terminal status after the SSE stream closes.
func (h *Handler) GetServerUpgrade(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing upgrade id")
		return
	}
	u, err := h.DB.GetUpgrade(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "upgrade not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := cobaltapi.ServerUpgrade{
		ID:            u.ID,
		TargetImage:   u.TargetImage,
		TargetVersion: u.TargetVersion,
		FromVersion:   u.FromVersion,
		Status:        u.Status,
		ErrorMessage:  u.ErrorMessage,
		StartedAt:     u.StartedAt,
	}
	if u.EndedAt != nil {
		out.EndedAt = *u.EndedAt
	}
	writeJSON(w, out)
}

// ServerUpgradeOutput implements GET /api/server/upgrade/{id}/output.
// Streams the helper container's structured log file as SSE — same
// pattern as deployment output. Closes when the upgrade hits a
// terminal status.
//
// Survives the daemon's own restart: the log file lives in cobalt-data
// (a docker-managed volume), and the new daemon comes back up with the
// same view of it. The CLI reconnects with Last-Event-ID to resume.
func (h *Handler) ServerUpgradeOutput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing upgrade id")
		return
	}
	u, err := h.DB.GetUpgrade(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "upgrade not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	offset, err := parseOffset(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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

	if err := h.streamUpgradeFile(r.Context(), sse, u.LogPath, offset, id); err != nil {
		h.Log.Warn("upgrade output stream", "upgrade_id", id, "error", err)
	}
}

// streamUpgradeFile follows the helper's log until the upgrade row
// reaches a terminal status. Mirrors streamDeployFile but checks the
// upgrades table for completion instead of deployments.
func (h *Handler) streamUpgradeFile(
	ctx context.Context,
	sse *sseWriter,
	path string,
	offset int64,
	upgradeID string,
) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		f, err = waitForFile(ctx, path, 5*time.Second)
		if err != nil {
			return nil
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
			u, derr := h.DB.GetUpgrade(ctx, upgradeID)
			if derr != nil {
				return derr
			}
			if u.IsTerminal() {
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

// upgradeLogPath returns the on-disk path the helper writes to and
// the daemon reads from. Lives under DataDir/upgrades/<id>.log so it's
// in the cobalt-data docker volume — survives the daemon restart that
// the upgrade itself causes.
func upgradeLogPath(dataDir, id string) string {
	return filepath.Join(dataDir, "upgrades", id+".log")
}

// newUpgradeID generates a short, URL-safe id. 8 bytes = 16 hex chars,
// plenty of entropy for cross-helper container names.
func newUpgradeID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "u_" + hex.EncodeToString(b[:])
}

// imageTagAsVersion extracts the tag suffix from an image ref so we
// can store something useful in target_version. Returns "" if the
// image has no tag (rare; defaults to :latest at runtime).
func imageTagAsVersion(image string) string {
	// Strip optional registry-host:port prefix; the LAST colon is the
	// tag separator.
	if i := strings.LastIndex(image, ":"); i > 0 {
		// Guard against `host:port/path` with no tag.
		if !strings.Contains(image[i:], "/") {
			return image[i+1:]
		}
	}
	return ""
}

// currentDaemonImage asks Swarm for the image of the running cobalt
// service. Used purely for logging — Swarm tracks the previous spec
// natively, so `docker service update --rollback` doesn't need this
// hint to revert. If lookup fails we just continue without it.
func currentDaemonImage(ctx context.Context, d *docker.Client) (string, error) {
	out, err := d.InspectServiceImage(ctx, swarmServiceName())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// swarmServiceName is the Swarm service name to update when self-
// upgrading. cobalt init deploys the stack as `cobalt`, so the
// service is `cobalt_cobalt`. Override via env var for forks or
// custom stack names.
func swarmServiceName() string {
	if v := os.Getenv("COBALT_SWARM_SERVICE_NAME"); v != "" {
		return v
	}
	return "cobalt_cobalt"
}

// dataVolumeName is the source-side name for the cobalt-data volume
// when constructing the helper container's bind mount. Stack-deploy
// prefixes the compose volume name with the stack name (so
// `cobalt-data` becomes `cobalt_cobalt-data`); compose-mode uses the
// bare name. Override via env var when the install diverges from the
// canonical `cobalt init` setup.
func dataVolumeName() string {
	if v := os.Getenv("COBALT_DATA_VOLUME"); v != "" {
		return v
	}
	return "cobalt_cobalt-data"
}
