package main

import (
	"fmt"

	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/spf13/cobra"
)

func newRollbackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Roll a project back to a previous deployment",
		Long: `Rolls a project back to a previous deployment by re-using its
cached docker image and cobaltfile snapshot. No clone, no rebuild —
just start services from the prior image, run healthchecks, swap
Caddy. Typically a 5-10s operation.

By default, rolls back to the most recent successful deployment that
isn't the current live one. Pass --to N to target a specific
deployment number.

Note: env vars are NOT versioned per deployment. A rollback runs
under whatever env vars are currently set on the project, not the
ones that existed when the target was originally built. Use
'cobalt env list' to verify the current state before rolling back.

Examples:
  cobalt rollback --project api
  cobalt rollback --project api --to 14
  cobalt rollback --project api --no-follow`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			to, _ := cmd.Flags().GetInt("to")
			noFollow, _ := cmd.Flags().GetBool("no-follow")

			if to > 0 {
				if err := confirm(
					cmd,
					fmt.Sprintf("Roll project %q back to deployment #%d?",
						pc.WrapProject(), to),
				); err != nil {
					return err
				}
			} else {
				if err := confirm(
					cmd,
					fmt.Sprintf("Roll project %q back to the previous successful deployment?",
						pc.WrapProject()),
				); err != nil {
					return err
				}
			}

			req := cobaltapi.RollbackRequest{To: to}
			d, err := pc.CreateRollback(cmd.Context(), pc.WrapProject(), req)
			if err != nil {
				return err
			}

			if noFollow {
				if output.IsJSON() {
					output.PrintJSON(d)
				} else {
					output.PrintLines(client.FormatDeployment(d))
				}
				return nil
			}

			status := string(d.Status)
			if err := output.FollowDeployOutput(cmd.Context(), pc.Client, d.ID, 0, output.Stdout); err != nil {
				if !cobaltapi.IsContextCanceled(err) {
					return err
				}
			}

			final, ferr := pc.GetDeployment(cmd.Context(), d.ID)
			if ferr == nil {
				status = string(final.Status)
			}

			colored := output.ColorStatus(status)
			output.PrintLines("")
			output.PrintLines("Rollback " + client.FormatDeployment(d) + " " + colored)

			if status == string(cobaltapi.StateFailed) {
				return fmt.Errorf("rollback failed")
			}
			return nil
		}),
	}
	cmd.Flags().String("project", "", "project name")
	cmd.Flags().Int("to", 0, "deployment number to roll back to (default: previous success)")
	cmd.Flags().Bool("no-follow", false, "enqueue and exit without waiting")
	return cmd
}
