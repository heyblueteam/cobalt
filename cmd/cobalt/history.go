package main

import (
	"strconv"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/spf13/cobra"
)

// newHistoryCmd is `cobalt history` — surfaces the project's
// `cobalt run` audit log so operators can see who ran what.
func newHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show cobalt run audit log for a project",
		Long: `Lists recent cobalt run invocations against a project, newest
first. Each row shows the command, who ran it (api key id), the
service, exit code, and start / finish times.

Use --limit to control how many rows are shown (default 20).

Examples:
  cobalt history --project api
  cobalt history --project api --limit 5 --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			limit, _ := cmd.Flags().GetInt("limit")
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			rows, err := pc.ListCommandRuns(cmd.Context(), pc.WrapProject(), limit)
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(rows)
				return nil
			}
			if len(rows) == 0 {
				output.PrintLines("No cobalt run invocations found.")
				return nil
			}
			headers := []string{"#", "STATUS", "EXIT", "TTY", "SERVICE", "COMMAND", "STARTED", "KEY"}
			var out [][]string
			for _, r := range rows {
				exit := "-"
				if r.Status == "finished" {
					exit = strconv.FormatInt(r.ExitCode, 10)
				}
				tty := "no"
				if r.TTY {
					tty = "yes"
				}
				keyID := "-"
				if r.APIKeyID > 0 {
					keyID = strconv.FormatInt(r.APIKeyID, 10)
				}
				out = append(out, []string{
					"#" + strconv.FormatInt(r.ID, 10),
					output.ColorStatus(r.Status),
					exit,
					tty,
					r.Service,
					trunc(r.Command, 50),
					fmtTime(r.CreatedAt),
					keyID,
				})
			}
			output.PrintTable(headers, out)
			return nil
		}),
	}
	cmd.Flags().String("project", "", "project name")
	cmd.Flags().Int("limit", 20, "max rows to show")
	return cmd
}

// trunc returns s clipped to n runes with an ellipsis. Cheap; not
// rune-aware (good enough for command lines).
func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
