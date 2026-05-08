package main

import (
	"fmt"

	"github.com/heyblueteam/cobalt/internal/client"
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
		Short: "Upgrade the daemon to a new image",
		Long: `Triggers a daemon upgrade. For v1 this is not yet implemented — the
daemon cannot self-upgrade while running.

To upgrade, manually run:
  docker service update --image ghcr.io/heyblueteam/cobalt:<tag> cobalt

Examples:
  cobalt meta upgrade
  cobalt meta upgrade --image ghcr.io/heyblueteam/cobalt:v2`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			image, _ := cmd.Flags().GetString("image")
			if image == "" {
				// Stub command: exit non-zero so scripts treat "nothing
				// happened" as an error and don't silently move on
				// thinking the daemon was upgraded.
				return fmt.Errorf("self-upgrade not implemented; run `docker service update --image <image> cobalt` on the daemon host")
			}
			err = c.UpgradeDaemon(cmd.Context(), client.UpgradeDaemonRequest{Image: image})
			if err != nil {
				return err
			}
			output.PrintLines("Upgrade triggered.")
			return nil
		}),
	}
	cmd.Flags().String("image", "", "image tag to upgrade to (not yet implemented)")
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
