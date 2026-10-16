package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/spf13/cobra"
)

func newDeploymentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "deployments",
		Aliases: []string{"deployment"},
		Short:   "Manage deployments",
	}
	cmd.PersistentFlags().String("project", "", "project name")
	cmd.AddCommand(
		newDeploymentsListCmd(),
		newDeploymentsCancelCmd(),
		newDeploymentsOutputCmd(),
	)
	return cmd
}

func newDeploymentsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List deployments",
		Long: `Lists recent deployments for a project.

Use --limit to control how many deployments are shown (default 20).

Examples:
  cobalt deployments list --project api
  cobalt deployments list --project api --limit 5 --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			limit, _ := cmd.Flags().GetInt("limit")
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			deps, err := pc.ListDeployments(cmd.Context(), pc.WrapProject(), limit)
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(deps)
				return nil
			}
			if len(deps) == 0 {
				output.PrintLines("No deployments found.")
				return nil
			}
			headers := []string{"#", "STATUS", "COMMIT", "CREATED"}
			var rows [][]string
			for _, d := range deps {
				commit := d.CommitSHA
				if len(commit) > 7 {
					commit = commit[:7]
				}
				rows = append(rows, []string{
					"#" + strconv.Itoa(d.Number),
					output.ColorStatus(string(d.Status)),
					commit,
					fmtTime(d.CreatedAt),
				})
			}
			output.PrintTable(headers, rows)
			return nil
		}),
	}
	cmd.Flags().Int("limit", 20, "max deployments to show")
	return cmd
}

// resolveDeployment picks the deployment a command acts on. Precedence:
// positional number (what `deployments list` shows, e.g. 957), then
// --deployment <internal id>, then the caller's fallback (most recent /
// most recent in-flight). Before this, a positional number was silently
// ignored and the fallback ran — `cancel 955` cancelled #956.
func resolveDeployment(
	cmd *cobra.Command,
	args []string,
	pc *projectClient,
	fallback func() (*cobaltapi.Deployment, error),
) (*cobaltapi.Deployment, error) {
	if len(args) == 1 {
		number, err := strconv.Atoi(strings.TrimPrefix(args[0], "#"))
		if err != nil {
			return nil, fmt.Errorf("invalid deployment number: %q (use the number from `deployments list`)", args[0])
		}
		return pc.DeploymentByNumber(cmd.Context(), pc.WrapProject(), number)
	}
	if deployment, _ := cmd.Flags().GetString("deployment"); deployment != "" {
		id, err := strconv.ParseInt(deployment, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid deployment id: %q", deployment)
		}
		return pc.GetDeployment(cmd.Context(), id)
	}
	return fallback()
}

func newDeploymentsCancelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel [number]",
		Short: "Cancel an in-flight deployment",
		Long: `Cancels a deployment by its number (as shown by 'deployments list'), or the
most recent in-flight one (queued, fetching, building, or swapping) when no
number is given. --deployment targets a specific deployment by internal ID.

Examples:
  cobalt deployments cancel 957 --project api
  cobalt deployments cancel --project api
  cobalt deployments cancel --project api --deployment 3542`,
		Args: cobra.MaximumNArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}

			targetDeployment, err := resolveDeployment(cmd, args, pc, func() (*cobaltapi.Deployment, error) {
				return pc.MostRecentInFlightDeployment(cmd.Context(), pc.WrapProject())
			})
			if err != nil {
				return err
			}

			if err := pc.CancelDeployment(cmd.Context(), targetDeployment.ID); err != nil {
				return err
			}
			output.PrintLines("Canceled deployment " + client.FormatDeployment(targetDeployment))
			return nil
		}),
	}
	cmd.Flags().String("deployment", "", "deployment id to cancel (defaults to most recent in-flight)")
	return cmd
}

func newDeploymentsOutputCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "output [number]",
		Short: "Stream deployment output",
		Long: `Streams the stdout/stderr output of a deployment in real time.

Give the deployment number (as shown by 'deployments list') to pick one;
without it, tails the most recent deployment. --deployment targets a specific
deployment by internal ID. Use Ctrl+C to stop.

Examples:
  cobalt deployments output 957 --project api
  cobalt deployments output --project api
  cobalt deployments output --project api --deployment 3542`,
		Args: cobra.MaximumNArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}

			targetDeployment, err := resolveDeployment(cmd, args, pc, func() (*cobaltapi.Deployment, error) {
				return pc.MostRecentDeployment(cmd.Context(), pc.WrapProject())
			})
			if err != nil {
				return err
			}

			if err := output.FollowDeployOutput(cmd.Context(), pc.Client, targetDeployment.ID, 0, output.Stdout); err != nil {
				if !cobaltapi.IsContextCanceled(err) {
					return err
				}
			}

			final, ferr := pc.GetDeployment(cmd.Context(), targetDeployment.ID)
			if ferr == nil {
				colored := output.ColorStatus(string(final.Status))
				output.PrintLines("")
				output.PrintLines("Status: " + colored)
			}

			return nil
		}),
	}
	cmd.Flags().String("deployment", "", "deployment id (defaults to most recent)")
	return cmd
}

func fmtTime(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).Format("2006-01-02 15:04:05")
}
