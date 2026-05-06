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
		newServerCmd(),
		newServersCmd(),
		newUseCmd(),
		newProjectsCmd(),
		newEnvCmd(),
		newDomainsCmd(),
		newScaleCmd(),
		newDeployCmd(),
		newDeploymentsCmd(),
		newGithubCmd(),
		newApikeysCmd(),
		newMetaCmd(),
		newVolumesCmd(),
		newLogsCmd(),
		newRunCmd(),
	)

	return cmd
}
