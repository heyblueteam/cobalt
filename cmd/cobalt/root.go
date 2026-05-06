package main

import (
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "cobalt",
		Short:         "Deployment glue for Docker Swarm + Caddy",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.AddCommand(
		newServerCmd(),
	)

	return cmd
}
