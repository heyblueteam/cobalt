package main

import (
	"fmt"
	"strconv"
	"strings"

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

func newDeploymentsCancelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel",
		Short: "Cancel an in-flight deployment",
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			deployment, _ := cmd.Flags().GetString("deployment")
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}

			var targetDeployment *cobaltapi.Deployment
			if deployment != "" {
				id, err := strconv.ParseInt(deployment, 10, 64)
				if err != nil {
					return fmt.Errorf("invalid deployment id: %q", deployment)
				}
				targetDeployment, err = pc.GetDeployment(cmd.Context(), id)
				if err != nil {
					return err
				}
			} else {
				targetDeployment, err = pc.MostRecentInFlightDeployment(cmd.Context(), pc.WrapProject())
				if err != nil {
					return err
				}
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
		Use:   "output",
		Short: "Stream deployment output",
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			deployment, _ := cmd.Flags().GetString("deployment")
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}

			var targetDeployment *cobaltapi.Deployment
			if deployment != "" {
				id, err := strconv.ParseInt(deployment, 10, 64)
				if err != nil {
					return fmt.Errorf("invalid deployment id: %q", deployment)
				}
				targetDeployment, err = pc.GetDeployment(cmd.Context(), id)
				if err != nil {
					return err
				}
			} else {
				targetDeployment, err = pc.MostRecentDeployment(cmd.Context(), pc.WrapProject())
				if err != nil {
					return err
				}
			}

			if err := output.FollowDeployOutput(cmd.Context(), pc.Client, targetDeployment.ID, 0, output.Stdout); err != nil {
				if !strings.Contains(err.Error(), "context canceled") {
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
	// Just print the raw Unix timestamp for now. A proper time formatting
	// helper can be added later.
	return fmt.Sprintf("%d", unix)
}
