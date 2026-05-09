package main

import (
	"github.com/spf13/cobra"
)

var version = "dev"

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "cobalt",
		Short:         "Deployment glue for Docker Swarm + Caddy",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().String("server", "", "cobalt server to use (defaults to ~/.cobalt/config.json defaultServer)")
	cmd.PersistentFlags().Bool("json", false, "output as JSON")
	cmd.PersistentFlags().Bool("yes", false, "skip confirmation prompts")

	cmd.AddCommand(
		newInitCmd(),
		newServerCmd(),
		newServerUpgradeHelperCmd(),
		newServersCmd(),
		newUseCmd(),
		newUpgradeCmd(),
		newProjectsCmd(),
		newEnvCmd(),
		newDomainsCmd(),
		newScaleCmd(),
		newDeployCmd(),
		newDeploymentsCmd(),
		newRollbackCmd(),
		newCronsCmd(),
		newGithubCmd(),
		newApikeysCmd(),
		newMetaCmd(),
		newVolumesCmd(),
		newLogsCmd(),
		newRunCmd(),
		newHistoryCmd(),
		newLlmCmd(),
	)

	return cmd
}
