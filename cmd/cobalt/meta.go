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
