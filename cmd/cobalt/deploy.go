package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/spf13/cobra"
)

func newDeployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Enqueue and follow a deployment",
		Long: `Triggers a deployment for a project. By default, follows the
deployment output until it finishes (like 'git push').

Use --no-follow to enqueue and exit immediately.`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			commit, _ := cmd.Flags().GetString("commit")
			noCache, _ := cmd.Flags().GetBool("no-cache")
			noFollow, _ := cmd.Flags().GetBool("no-follow")
			force, _ := cmd.Flags().GetBool("force")
			filePath, _ := cmd.Flags().GetString("file")

			req := cobaltapi.DeploymentCreateRequest{
				Commit:  commit,
				NoCache: noCache,
				Force:   force,
			}

			if filePath != "" {
				data, err := os.ReadFile(filePath)
				if err != nil {
					return fmt.Errorf("read file %q: %w", filePath, err)
				}
				if !json.Valid(data) {
					return fmt.Errorf("file %q is not valid JSON", filePath)
				}
				req.CobaltfileOverride = string(data)
			}

			resp, err := pc.CreateDeployment(cmd.Context(), pc.WrapProject(), req)
			if err != nil {
				return err
			}
			d := &resp.Deployment

			if resp.CancelledInflightId != 0 && !output.IsJSON() {
				output.PrintLines(fmt.Sprintf("Cancelled in-flight deployment %d", resp.CancelledInflightId))
			}

			if noFollow {
				if output.IsJSON() {
					output.PrintJSON(resp)
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
			output.PrintLines("Deployment " + client.FormatDeployment(d) + " " + colored)

			if status == string(cobaltapi.StateFailed) {
				return fmt.Errorf("deployment failed")
			}
			return nil
		}),
	}
	cmd.Flags().String("project", "", "project name")
	cmd.Flags().String("commit", "", "git commit to deploy (defaults to latest on branch)")
	cmd.Flags().Bool("no-cache", false, "disable docker build cache")
	cmd.Flags().Bool("no-follow", false, "enqueue and exit without waiting")
	cmd.Flags().Bool("force", false, "cancel any in-flight fetch/build for this project before enqueuing (rejected if the in-flight deploy is already in cutover)")
	cmd.Flags().String("file", "", "path to a cobalt.json override file")
	return cmd
}
