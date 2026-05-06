package main

import (
	"github.com/heyblueteam/cobalt/internal/cliconfig"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/spf13/cobra"
)

func newUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <project>",
		Short: "Set the current project for the active server",
		Args:  cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			project := args[0]

			cpath, err := cliconfig.DefaultPath()
			if err != nil {
				return err
			}
			cfg, err := cliconfig.Load(cpath)
			if err != nil {
				return err
			}
			explicit := cmd.Flag("server").Value.String()
			if f := cmd.Flags().Lookup("server"); f != nil && !f.Changed {
				explicit = ""
			}
			name, _, err := cfg.Active(explicit)
			if err != nil {
				return err
			}
			if err := cfg.SetCurrentProject(name, project); err != nil {
				return err
			}
			if err := cliconfig.Save(cpath, cfg); err != nil {
				return err
			}
			output.PrintLines("Current project set to " + project + " for server " + name)
			return nil
		}),
	}
}
