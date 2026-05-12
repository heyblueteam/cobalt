package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/spf13/cobra"
)

// defaultUpgradeImageRepo is where official cobalt images live. Used
// when the operator passes --to vX.Y.Z without --image. Forks/custom
// registries pass --image directly.
const defaultUpgradeImageRepo = "ghcr.io/heyblueteam/cobalt"

// newUpgradeCmd wires the operator-facing self-upgrade command.
//
// Behavior:
//   - --to <version> → resolves to defaultUpgradeImageRepo:<version>
//   - --image <ref>   → uses that ref directly (custom registries, forks)
//   - At least one is required.
//   - Preflight: prints current vs target version, refuses no-ops
//     unless --force is set.
//   - Follows the SSE stream until the upgrade reaches a terminal
//     status, then exits 0 (succeeded) or non-zero (failed/rolled-back).
func newUpgradeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade the cobalt daemon to a new version",
		Long: `Upgrades the cobalt daemon on the configured server.

Triggers a daemon-side helper container that pulls the target image,
swaps the cobalt service, probes the new daemon's health, and rolls
back automatically if the new daemon doesn't come up within 90s.

Examples:
  cobalt upgrade --to v0.9.0
  cobalt upgrade --image ghcr.io/myorg/my-cobalt-fork:v1.2.3
  cobalt upgrade --to v0.9.0 --server prod`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			to, _ := cmd.Flags().GetString("to")
			image, _ := cmd.Flags().GetString("image")
			force, _ := cmd.Flags().GetBool("force")
			noFollow, _ := cmd.Flags().GetBool("no-follow")

			if to == "" && image == "" {
				return fmt.Errorf("one of --to or --image is required")
			}
			if to != "" && image != "" {
				return fmt.Errorf("--to and --image are mutually exclusive")
			}
			if image == "" {
				image = defaultUpgradeImageRepo + ":" + to
			}
			return runUpgrade(cmd, image, force, noFollow)
		}),
	}
	cmd.Flags().String("to", "", "target version (e.g. v0.9.0); resolves to "+defaultUpgradeImageRepo+":<version>")
	cmd.Flags().String("image", "", "explicit image reference (escape hatch for custom registries)")
	cmd.Flags().Bool("force", false, "proceed even when the daemon is already at the target version")
	cmd.Flags().Bool("no-follow", false, "exit immediately after triggering, without streaming the helper log")
	return cmd
}

// runUpgrade is the shared upgrade pipeline used by `cobalt upgrade` and
// the deprecated alias `cobalt meta upgrade`. Both surface the same
// preflight (current vs target version), backend (POST /api/server/upgrade),
// and follow semantics (SSE → terminal status → exit code).
func runUpgrade(cmd *cobra.Command, image string, force, noFollow bool) error {
	cl, err := newClient(cmd)
	if err != nil {
		return err
	}
	info, err := cl.GetMetaInfo(cmd.Context())
	if err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	targetVersion := imageTagOnly(image)
	output.PrintLines("Current daemon version: " + info.Version)
	output.PrintLines("Target image:           " + image)
	if !force && targetVersion != "" && targetVersion == info.Version {
		return fmt.Errorf("daemon is already at %s; pass --force to upgrade anyway",
			info.Version)
	}
	u, err := cl.CreateUpgrade(cmd.Context(), cobaltapi.ServerUpgradeRequest{
		Image: image,
		Pull:  true,
	})
	if err != nil {
		return err
	}
	output.PrintLines("Upgrade " + u.ID + " started")
	if noFollow {
		if output.IsJSON() {
			output.PrintJSON(u)
		}
		return nil
	}
	return followUpgrade(cmd.Context(), cl, u.ID)
}

// followUpgrade streams the helper log via SSE until a terminal
// status, then exits with the corresponding code.
func followUpgrade(ctx context.Context, cl *client.Client, id string) error {
	resp, err := cl.UpgradeOutput(ctx, id)
	if err != nil {
		return fmt.Errorf("open log stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("log stream returned %s", resp.Status)
	}
	streamErr := output.ConsumeSSE(ctx, resp.Body, output.Stdout)
	// SSE close happens for two reasons during a real upgrade: the
	// daemon serving the stream got swapped (TCP reset on the old
	// container), or the upgrade really finished. Either way we now
	// poll for terminal status — generous deadline because the helper
	// can still be finishing post-stream work (probe, status file
	// write, daemon reconcile of the file).
	final, err := getUpgradeWithRetry(ctx, cl, id, 3*time.Minute)
	if err != nil {
		// Stream may have closed mid-restart; surface the streamErr if
		// it's the more useful one.
		if streamErr != nil {
			return fmt.Errorf("upgrade follow: %w (stream error: %v)", err, streamErr)
		}
		return fmt.Errorf("post-stream status fetch: %w", err)
	}
	output.PrintLines("")
	output.PrintLines("Upgrade " + final.ID + ": " + final.Status)
	switch final.Status {
	case cobaltapi.UpgradeStatusSucceeded:
		return nil
	case cobaltapi.UpgradeStatusRolledBack:
		return fmt.Errorf("upgrade failed and rolled back: %s", final.ErrorMessage)
	case cobaltapi.UpgradeStatusFailed:
		return fmt.Errorf("upgrade failed: %s", final.ErrorMessage)
	default:
		return fmt.Errorf("upgrade %s: unexpected terminal status %q", final.ID, final.Status)
	}
}

// getUpgradeWithRetry polls GetUpgrade until the row reaches a
// terminal status, the daemon stops responding, or the timeout
// elapses. Two distinct retry concerns this folds together:
//
//   - the daemon is briefly unavailable across the restart that the
//     upgrade itself causes (transport error, retry)
//   - the helper is still finishing post-restart work — sentinel
//     file not yet written, status reconciliation hasn't fired
//     (status=running, retry)
//
// Returns the row only when IsTerminal() is true, or with the last
// error when the deadline passes.
func getUpgradeWithRetry(ctx context.Context, cl *client.Client, id string, timeout time.Duration) (*cobaltapi.ServerUpgrade, error) {
	deadline := time.Now().Add(timeout)
	var lastResult *cobaltapi.ServerUpgrade
	var lastErr error
	for {
		u, err := cl.GetUpgrade(ctx, id)
		if err == nil {
			lastResult = u
			if u.IsTerminal() {
				return u, nil
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastResult != nil {
				// Daemon was reachable but the row never went
				// terminal within the budget. Surface the row as-is
				// so the caller can produce a useful error message.
				return lastResult, nil
			}
			return nil, lastErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// imageTagOnly returns the tag portion of an image ref, or "" if
// none. Mirrors the daemon-side imageTagAsVersion.
func imageTagOnly(image string) string {
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
