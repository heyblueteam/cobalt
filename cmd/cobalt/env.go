package main

import (
	"fmt"
	"strings"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/spf13/cobra"
)

func newEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Manage environment variables",
	}
	cmd.PersistentFlags().String("project", "", "project name")
	cmd.AddCommand(
		newEnvListCmd(),
		newEnvSetCmd(),
		newEnvRemoveCmd(),
	)
	return cmd
}

func newEnvListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List environment variables",
		Long: `Lists all environment variables for a project.

Examples:
  cobalt env list --project api
  cobalt env list --project api --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			vars, err := pc.ListEnvVars(cmd.Context(), pc.WrapProject())
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(vars)
				return nil
			}
			if len(vars) == 0 {
				output.PrintLines("No env vars set.")
				return nil
			}
			headers := []string{"KEY", "VALUE"}
			var rows [][]string
			for _, v := range vars {
				rows = append(rows, []string{v.Key, v.Value})
			}
			output.PrintTable(headers, rows)
			return nil
		}),
	}
}

func newEnvSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set KEY=VAL [KEY2=VAL2 ...]",
		Short: "Set environment variables",
		Long: `Upserts one or more environment variables. Existing keys are overwritten.

Use --redeploy to automatically enqueue a deployment after updating env vars —
this is the typical workflow when changing configuration that a running service
needs to pick up.

Examples:
  cobalt env set NODE_ENV=production --project api
  cobalt env set FOO=bar BAZ=qux --project api --redeploy`,
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("at least one KEY=VAL pair is required")
			}
			vars := make(map[string]string)
			for _, kv := range args {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid format: %q — expected KEY=VAL", kv)
				}
				vars[parts[0]] = parts[1]
			}
			redeploy, _ := cmd.Flags().GetBool("redeploy")
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			req := cobaltapi.EnvSetRequest{
				Vars:     vars,
				Redeploy: redeploy,
			}
			result, err := pc.SetEnvVars(cmd.Context(), pc.WrapProject(), req)
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(result)
				return nil
			}
			for _, v := range result {
				output.PrintLines(fmt.Sprintf("Set %s=%s", v.Key, v.Value))
			}
			return nil
		}),
	}
	cmd.Flags().Bool("redeploy", false, "enqueue a fresh deployment after the env change")
	return cmd
}

func newEnvRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <KEY>",
		Short: "Remove an environment variable",
		Long: `Removes an environment variable from a project. Use --yes to skip the
confirmation prompt.

Examples:
  cobalt env remove FOO --project api --yes`,
		Args:  cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if err := confirm(cmd, "Remove env var \""+key+"\"?"); err != nil {
				return err
			}
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			return pc.DeleteEnvVar(cmd.Context(), pc.WrapProject(), key)
		}),
	}
	return cmd
}
