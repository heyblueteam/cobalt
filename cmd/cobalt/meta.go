package main

import (
	"fmt"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/spf13/cobra"
)

func newMetaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "meta",
		Short: "Daemon information",
	}
	cmd.AddCommand(newMetaInfoCmd())
	cmd.AddCommand(newMetaUpgradeCmd())
	cmd.AddCommand(newMetaHostCmd())
	return cmd
}

func newMetaInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show daemon version and uptime",
		Long: `Prints daemon metadata: version, hostname, uptime, and start time.
Useful for incident response and verifying which daemon you're talking to.

Examples:
  cobalt meta info
  cobalt meta info --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			info, err := c.GetMetaInfo(cmd.Context())
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(info)
				return nil
			}
			output.PrintKeyValue(
				[2]string{"Version", info.Version},
				[2]string{"Hostname", info.Hostname},
				[2]string{"Uptime", formatDuration(info.UptimeSecs)},
				[2]string{"Started at", fmt.Sprintf("%d", info.StartedAt)},
			)
			return nil
		}),
	}
}

func formatDuration(secs int64) string {
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	remainSecs := secs % 60
	if mins < 60 {
		return fmt.Sprintf("%dm%ds", mins, remainSecs)
	}
	hours := mins / 60
	remainMins := mins % 60
	if hours < 24 {
		return fmt.Sprintf("%dh%dm", hours, remainMins)
	}
	days := hours / 24
	remainHours := hours % 24
	return fmt.Sprintf("%dd%dh", days, remainHours)
}

func newMetaUpgradeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "[DEPRECATED] Upgrade the daemon to a new image (use `cobalt upgrade`)",
		Long: `Deprecated alias for ` + "`cobalt upgrade`" + `. Both end up calling the same
daemon-side helper that pulls the target image, swaps the cobalt
service, probes health, and rolls back if the new daemon doesn't
come up within 90s. Prefer the top-level command — this alias will
be removed once every supported install has the newer CLI.

Examples:
  cobalt meta upgrade --image ghcr.io/heyblueteam/cobalt:v0.9.0
  cobalt meta upgrade --image ghcr.io/myorg/my-fork:v1.2.3 --no-follow`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			image, _ := cmd.Flags().GetString("image")
			if image == "" {
				return fmt.Errorf("--image is required")
			}
			force, _ := cmd.Flags().GetBool("force")
			noFollow, _ := cmd.Flags().GetBool("no-follow")
			output.PrintLines("Note: `cobalt meta upgrade` is deprecated — prefer `cobalt upgrade --image <ref>`.")
			return runUpgrade(cmd, image, force, noFollow)
		}),
	}
	cmd.Flags().String("image", "", "image reference to upgrade to (e.g. ghcr.io/heyblueteam/cobalt:v0.9.0)")
	cmd.Flags().Bool("force", false, "proceed even when the daemon is already at the target version")
	cmd.Flags().Bool("no-follow", false, "exit immediately after triggering, without streaming the helper log")
	return cmd
}

func newMetaHostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host <hostname>",
		Short: "Update the daemon's public hostname",
		Long: `Updates the daemon's public hostname used in GitHub App manifest URLs
and other externally-facing addresses. Propagates the change to Caddy's
route matcher.

Examples:
  cobalt meta host cobalt.blue.cc
  cobalt meta host api.blue.cc`,
		Args: cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			host := args[0]
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			info, err := c.SetMetaHost(cmd.Context(), host)
			if err != nil {
				return err
			}
			output.PrintLines("Host updated to " + info.Hostname)
			return nil
		}),
	}
	return cmd
}
