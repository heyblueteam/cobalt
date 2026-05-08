package main

import (
	"fmt"
	"strings"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
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
		newEnvGetCmd(),
		newEnvSetCmd(),
		newEnvRemoveCmd(),
	)
	return cmd
}

func newEnvListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List environment variables",
		Long: `Lists all environment variables for a project.

Values are redacted by default to avoid accidental shoulder-surfing
in screen-shares; pass --show-values (or --json) to print plaintext.

Examples:
  cobalt env list --project api
  cobalt env list --project api --show-values
  cobalt env list --project api --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			showValues, _ := cmd.Flags().GetBool("show-values")
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			vars, err := pc.ListEnvVars(cmd.Context(), pc.WrapProject())
			if err != nil {
				return err
			}
			if output.IsJSON() {
				// JSON output is structured / scriptable; if the
				// caller is piping through jq they want real values.
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
				val := v.Value
				if !showValues {
					val = redactEnvValue(v.Value)
				}
				rows = append(rows, []string{v.Key, val})
			}
			output.PrintTable(headers, rows)
			return nil
		}),
	}
	cmd.Flags().Bool("show-values", false, "print env values instead of redacting them")
	return cmd
}

// redactEnvValue obscures a value for table rendering. Empty stays
// empty (an unset value is meaningful); short values get a fixed
// mask so length isn't leaked for things like 4-char tokens; longer
// values show a length hint.
func redactEnvValue(v string) string {
	switch {
	case v == "":
		return ""
	case len(v) <= 8:
		return "***"
	default:
		return "*** (" + itoa(len(v)) + " chars)"
	}
}

func itoa(n int) string {
	// Tiny inline to avoid pulling strconv just for the redaction
	// width hint; keep this file's import set short.
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func newEnvGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <KEY>",
		Short: "Get a single environment variable",
		Long: `Prints the value of a single environment variable.

Examples:
  cobalt env get NODE_ENV --project api
  cobalt env get API_KEY --project api --json`,
		Args: cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if err := validator.ValidateEnvKey(key); err != nil {
				return err
			}
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			envVar, err := pc.GetEnvVar(cmd.Context(), pc.WrapProject(), key)
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(envVar)
				return nil
			}
			output.PrintLines(fmt.Sprintf("%s=%s", envVar.Key, envVar.Value))
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
				if err := validator.ValidateEnvKey(parts[0]); err != nil {
					return err
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
			if !redeploy {
				output.PrintLines("")
				output.PrintLines("Note: live containers won't see this until next deploy.")
				output.PrintLines("      Pass --redeploy to deploy now, or run `cobalt deploy`.")
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
confirmation prompt. Pass --redeploy to apply the removal to live
containers immediately.

Examples:
  cobalt env remove FOO --project api --yes
  cobalt env remove FOO --project api --yes --redeploy`,
		Args:  cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if err := validator.ValidateEnvKey(key); err != nil {
				return err
			}
			if err := confirm(cmd, "Remove env var \""+key+"\"?"); err != nil {
				return err
			}
			redeploy, _ := cmd.Flags().GetBool("redeploy")
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			if err := pc.DeleteEnvVar(cmd.Context(), pc.WrapProject(), key, redeploy); err != nil {
				return err
			}
			if !output.IsJSON() && !redeploy {
				output.PrintLines("")
				output.PrintLines("Note: live containers won't see this until next deploy.")
				output.PrintLines("      Pass --redeploy to deploy now, or run `cobalt deploy`.")
			}
			return nil
		}),
	}
	cmd.Flags().Bool("redeploy", false, "enqueue a fresh deployment after the env change")
	return cmd
}
