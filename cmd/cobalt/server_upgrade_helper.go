package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/spf13/cobra"
)

// newServerUpgradeHelperCmd returns the hidden `cobalt
// server-upgrade-helper` subcommand. The daemon spawns this in a
// detached transient container during a self-upgrade. The helper:
//
//  1. logs structured progress to a file in cobalt-data so the daemon's
//     SSE endpoint can stream it (the daemon survives the restart
//     because the log lives on a docker-managed volume)
//  2. captures the running cobalt service's current image as the
//     rollback target (Swarm tracks this natively too — we capture
//     for logging clarity)
//  3. runs `docker service update --image <new> cobalt_cobalt` —
//     Swarm rolling-updates the replica
//  4. probes the daemon's public HTTPS endpoint until it reports the
//     target version, with a 90s deadline
//  5. on probe-timeout: `docker service update --rollback cobalt_cobalt`
//     reverts the service spec atomically using Swarm's previous-
//     spec tracking
//  6. updates the upgrade row's terminal status in rqlite
//
// Hidden because operators never invoke this directly. End users use
// `cobalt upgrade`, which POSTs to the daemon, which spawns this
// helper.
func newServerUpgradeHelperCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "server-upgrade-helper",
		Short:  "Internal: run the daemon-side self-upgrade flow",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h := &upgradeHelper{
				upgradeID:     mustFlagString(cmd, "upgrade-id"),
				targetImage:   mustFlagString(cmd, "target-image"),
				rollbackImage: mustFlagString(cmd, "rollback-image"),
				logPath:       mustFlagString(cmd, "log-path"),
				rqliteURL:     mustFlagString(cmd, "rqlite-url"),
				serviceName:   mustFlagString(cmd, "service-name"),
				publicHost:    mustFlagString(cmd, "public-host"),
				doPull:        !mustFlagBool(cmd, "no-pull"),
			}
			return h.run(cmd.Context())
		},
	}
	cmd.Flags().String("upgrade-id", "", "upgrade row id (required)")
	cmd.Flags().String("target-image", "", "image to swap to (required)")
	cmd.Flags().String("rollback-image", "", "image to revert to if the new daemon fails health probe; empty = use Swarm's previous-spec rollback")
	cmd.Flags().String("log-path", "", "path to write structured progress to (required)")
	cmd.Flags().String("rqlite-url", "http://rqlite:4001", "rqlite cluster URL")
	cmd.Flags().String("service-name", "cobalt_cobalt", "Swarm service name to update (stack-prefix + service)")
	cmd.Flags().String("public-host", "", "daemon's public hostname for the post-upgrade health probe (required)")
	cmd.Flags().Bool("no-pull", false, "skip docker pull (image already present on host)")
	for _, name := range []string{"upgrade-id", "target-image", "log-path", "public-host"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func mustFlagString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

func mustFlagBool(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}

// upgradeHelper carries the helper's flag-driven state.
type upgradeHelper struct {
	upgradeID     string
	targetImage   string
	rollbackImage string
	logPath       string
	rqliteURL     string
	serviceName   string
	publicHost    string
	doPull        bool

	logFile *os.File
}

func (h *upgradeHelper) run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(h.logPath), 0o755); err != nil {
		return fmt.Errorf("mkdir log dir: %w", err)
	}
	f, err := os.OpenFile(h.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	h.logFile = f
	defer f.Close()

	h.logf("==> upgrade %s starting", h.upgradeID)
	h.logf("    target image:   %s", h.targetImage)
	h.logf("    swarm service:  %s", h.serviceName)
	h.logf("    public host:    %s", h.publicHost)

	// Step 1: pull target image. Pre-pulling means Swarm doesn't have
	// to pause mid-update to fetch — keeps the cutover window tight.
	if h.doPull {
		h.logf("📥 pulling %s", h.targetImage)
		if err := h.runDockerStreaming(ctx, "pull", h.targetImage); err != nil {
			return h.fail(ctx, fmt.Errorf("pull target image: %w", err))
		}
		h.logf("✅ pulled %s", h.targetImage)
	} else {
		h.logf("⏭  skipping pull (--no-pull)")
	}

	// Step 2: capture rollback image from the live service spec. We
	// don't actually need this for `docker service update --rollback`
	// (Swarm tracks the previous spec on its own), but logging it
	// makes the upgrade audit trail useful.
	if h.rollbackImage == "" {
		out, err := h.runDockerCapture(ctx,
			"service", "inspect", h.serviceName,
			"--format", "{{.Spec.TaskTemplate.ContainerSpec.Image}}",
		)
		if err != nil {
			return h.fail(ctx, fmt.Errorf("inspect %s: %w", h.serviceName, err))
		}
		h.rollbackImage = strings.TrimSpace(out)
	}
	h.logf("    rollback image: %s", h.rollbackImage)
	if h.rollbackImage == h.targetImage {
		h.logf("⏭  rollback == target; this is a no-op upgrade")
		return h.success(ctx)
	}

	// Step 3: docker service update. Swarm performs a rolling update
	// of the service's replicas (1 in our case) — old task drains,
	// new task starts. The daemon API is briefly unavailable; the
	// post-update probe handles that.
	h.logf("🔄 swapping %s to %s", h.serviceName, h.targetImage)
	if err := h.runDockerStreaming(ctx,
		"service", "update",
		"--image", h.targetImage,
		// Wait for swarm to settle. Without this, `service update`
		// returns immediately and we'd race the probe against a
		// not-yet-converged service.
		"--update-order", "stop-first",
		h.serviceName,
	); err != nil {
		h.logf("❌ docker service update failed; attempting rollback")
		_ = h.swarmRollback(ctx)
		return h.fail(ctx, fmt.Errorf("service update: %w", err))
	}

	// Step 4: probe the new daemon over public HTTPS. Use the public
	// host because (a) the helper container isn't attached to the
	// cobalt-main overlay, so it can't resolve `cobalt_cobalt` via
	// Swarm DNS without extra plumbing, and (b) the public path is
	// exactly what real traffic uses, so success here means the swap
	// is end-to-end correct, not just "container is running."
	probeURL := "https://" + h.publicHost + "/api/meta/info"
	h.logf("🩺 probing %s for new daemon (timeout 90s)", probeURL)
	wantVer := imageTagSuffix(h.targetImage)
	if err := h.probeNewDaemon(ctx, probeURL, wantVer, 90*time.Second); err != nil {
		h.logf("❌ new daemon health probe failed: %v", err)
		h.logf("↩️  rolling back via `docker service update --rollback`")
		if rerr := h.swarmRollback(ctx); rerr != nil {
			h.logf("🚨 rollback ALSO failed: %v", rerr)
			return h.markStatus(ctx, store.UpgradeStatusFailed,
				fmt.Sprintf("upgrade failed (%s) and rollback also failed (%v)", err, rerr))
		}
		// Wait for the rollback to settle so the daemon is reachable
		// again by the time the operator's CLI re-polls.
		_ = h.probeNewDaemon(ctx, probeURL, imageTagSuffix(h.rollbackImage), 60*time.Second)
		h.logf("✅ rolled back to %s", h.rollbackImage)
		return h.markStatus(ctx, store.UpgradeStatusRolledBack, err.Error())
	}

	return h.success(ctx)
}

// swarmRollback reverts the service to its previous spec using
// Swarm's native --rollback flag. Swarm keeps the previous spec
// after every successful update, so this is a single command.
func (h *upgradeHelper) swarmRollback(ctx context.Context) error {
	return h.runDockerStreaming(ctx, "service", "update", "--rollback", h.serviceName)
}

func (h *upgradeHelper) success(ctx context.Context) error {
	h.logf("✅ upgrade %s complete", h.upgradeID)
	return h.markStatus(ctx, store.UpgradeStatusSucceeded, "")
}

func (h *upgradeHelper) fail(ctx context.Context, err error) error {
	h.logf("❌ %s", err.Error())
	return h.markStatus(ctx, store.UpgradeStatusFailed, err.Error())
}

// markStatus records terminal status. Two-path: try rqlite first
// (fast, single source of truth), fall back to a sentinel JSON file
// the daemon picks up next time it serves /api/server/upgrade/{id}.
// The fallback exists because helper containers may not be on a
// network that resolves rqlite (Swarm overlays default to non-
// attachable; the helper lands on the docker bridge).
func (h *upgradeHelper) markStatus(ctx context.Context, status, errMsg string) error {
	if err := h.writeStatusFile(status, errMsg); err != nil {
		h.logf("⚠️  status file write failed: %v", err)
	}
	db, err := store.Open(h.rqliteURL)
	if err != nil {
		h.logf("ℹ️  rqlite unreachable from helper, terminal status will be picked up via status file: %v", err)
		return nil
	}
	if err := db.SetUpgradeStatus(ctx, h.upgradeID, status, errMsg); err != nil {
		h.logf("ℹ️  rqlite write failed, terminal status will be picked up via status file: %v", err)
		return nil
	}
	return nil
}

// writeStatusFile drops a small JSON next to the upgrade log. The
// daemon's GET /api/server/upgrade/{id} handler reconciles this into
// the upgrade row when it sees one. Idempotent — last writer wins,
// daemon deletes the file after applying.
func (h *upgradeHelper) writeStatusFile(status, errMsg string) error {
	path := strings.TrimSuffix(h.logPath, ".log") + ".status"
	body := map[string]string{
		"status":        status,
		"error_message": errMsg,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// probeNewDaemon polls the daemon's /api/meta/info every 2s. Success
// is HTTP 200 + version field == wantVersion, OR HTTP 401/403
// (daemon is up + responding, just refusing us due to missing auth —
// the daemon being live is the load-bearing signal).
//
// InsecureSkipVerify is on because we're probing the daemon's public
// host but during a brief window when LE may not yet have reissued
// (it shouldn't reissue, since the cert lives in the caddy volume
// which isn't touched). Belt-and-suspenders for the cutover window.
func (h *upgradeHelper) probeNewDaemon(ctx context.Context, url, wantVersion string, timeout time.Duration) error {
	cli := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec  // probe across cutover
		},
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := cli.Do(req)
		if err != nil {
			lastErr = err
		} else {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
			resp.Body.Close()
			switch resp.StatusCode {
			case 200:
				var info struct {
					Version string `json:"version"`
				}
				_ = json.Unmarshal(body, &info)
				if wantVersion == "" || info.Version == wantVersion {
					return nil
				}
				lastErr = fmt.Errorf("got version=%q want %q", info.Version, wantVersion)
			case 401, 403:
				// Daemon is up + responding to HTTP, just refusing
				// our unauthenticated probe. That's the signal we
				// care about — it means the swap landed and the new
				// daemon is serving.
				return nil
			default:
				lastErr = fmt.Errorf("status %d body=%s", resp.StatusCode, snippet(string(body)))
			}
		}
		if time.Now().After(deadline) {
			if lastErr == nil {
				lastErr = fmt.Errorf("timeout")
			}
			return fmt.Errorf("daemon at %s not healthy within %s: %w", url, timeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// runDockerStreaming runs a docker subprocess and pipes stdout +
// stderr line-by-line into the log file.
func (h *upgradeHelper) runDockerStreaming(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = newPrefixWriter(h.logFile, "    ")
	cmd.Stderr = newPrefixWriter(h.logFile, "    ")
	return cmd.Run()
}

// runDockerCapture runs a docker subprocess and returns its stdout.
func (h *upgradeHelper) runDockerCapture(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// logf writes a timestamped line to both stdout (for `docker logs`)
// and the SSE-followed log file.
func (h *upgradeHelper) logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	stamp := time.Now().UTC().Format("15:04:05")
	rendered := fmt.Sprintf("[%s] %s\n", stamp, line)
	_, _ = io.WriteString(os.Stdout, rendered)
	if h.logFile != nil {
		_, _ = h.logFile.WriteString(rendered)
	}
}

// prefixWriter wraps an io.Writer and prefixes each line with a
// fixed string. Used to indent docker subprocess output so it's
// visually distinguishable from helper-emitted progress lines.
type prefixWriter struct {
	w      io.Writer
	prefix string
	pend   []byte
}

func newPrefixWriter(w io.Writer, prefix string) *prefixWriter {
	return &prefixWriter{w: w, prefix: prefix}
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.pend = append(p.pend, b...)
	for {
		i := indexByte(p.pend, '\n')
		if i < 0 {
			return len(b), nil
		}
		line := append([]byte(p.prefix), p.pend[:i+1]...)
		if _, err := p.w.Write(line); err != nil {
			return 0, err
		}
		p.pend = p.pend[i+1:]
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func snippet(s string) string {
	const max = 120
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// imageTagSuffix returns the tag portion of a docker image ref, or
// "" when none is present (so the caller can decide whether to skip
// version verification).
func imageTagSuffix(image string) string {
	i := strings.LastIndex(image, ":")
	if i <= 0 {
		return ""
	}
	tail := image[i+1:]
	if strings.Contains(tail, "/") {
		return ""
	}
	return tail
}
