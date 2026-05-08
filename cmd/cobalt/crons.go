package main

import (
	"time"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/spf13/cobra"
)

func newCronsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "crons",
		Aliases: []string{"cron"},
		Short:   "Manage project cron services",
	}
	cmd.AddCommand(newCronsListCmd())
	return cmd
}

func newCronsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List cron services registered for the project",
		Long: `Lists every cron service the daemon's scheduler currently has
registered for the project, with the next time each is due to fire.

The schedule + command columns reflect the most recent successful
deployment's cobaltfile. Crons are reconciled on every deploy, so
this list updates within a second of a cutover.

Empty list = no cron services declared (in the live cobaltfile) for
this project.

Examples:
  cobalt crons list
  cobalt crons list --project api
  cobalt crons list --project api --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			rows, err := pc.ListProjectCrons(cmd.Context(), pc.WrapProject())
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(rows)
				return nil
			}
			if len(rows) == 0 {
				output.PrintLines("No cron services registered for project " + pc.WrapProject() + ".")
				return nil
			}
			data := make([][]string, 0, len(rows))
			for _, r := range rows {
				next := "-"
				if r.NextFireAt > 0 {
					next = time.Unix(r.NextFireAt, 0).Local().Format("2006-01-02 15:04:05")
				}
				data = append(data, []string{r.Service, r.Schedule, next, r.Command})
			}
			output.PrintTable([]string{"SERVICE", "SCHEDULE", "NEXT FIRE", "COMMAND"}, data)
			return nil
		}),
	}
	cmd.Flags().String("project", "", "project name")
	return cmd
}
