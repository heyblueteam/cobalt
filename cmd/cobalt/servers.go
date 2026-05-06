package main

import (
	"github.com/heyblueteam/cobalt/internal/cliconfig"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/spf13/cobra"
)

func newServersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "servers",
		Aliases: []string{"server"},
		Short:   "Manage configured cobalt servers",
	}

	cmd.AddCommand(
		newServersListCmd(),
		newServersRemoveCmd(),
	)

	return cmd
}

func newServersListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured servers",
		Long: `Lists all cobalt servers stored in ~/.cobalt/config.json.

The active (default) server is marked with an asterisk. Each entry shows the
server name, host URL, and current project.

Examples:
  cobalt servers list
  cobalt servers list --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			cpath, err := cliconfig.DefaultPath()
			if err != nil {
				return err
			}
			cfg, err := cliconfig.Load(cpath)
			if err != nil {
				return err
			}

			if output.IsJSON() {
				type serverEntry struct {
					Name               string `json:"name"`
					Host               string `json:"host"`
					CurrentProject     string `json:"currentProject,omitempty"`
					IsDefault          bool   `json:"isDefault"`
				}
				var entries []serverEntry
				for name, s := range cfg.Servers {
					entries = append(entries, serverEntry{
						Name:           name,
						Host:           s.Host,
						CurrentProject: s.CurrentProject,
						IsDefault:      name == cfg.DefaultServer,
					})
				}
				output.PrintJSON(entries)
				return nil
			}

			headers := []string{"NAME", "HOST", "PROJECT", ""}
			var rows [][]string
			for name, s := range cfg.Servers {
				marker := ""
				if name == cfg.DefaultServer {
					marker = "*"
				}
				rows = append(rows, []string{name, s.Host, s.CurrentProject, marker})
			}
			output.PrintTable(headers, rows)
			return nil
		}),
	}
}

func newServersRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a configured server",
		Long: `Removes a server entry from ~/.cobalt/config.json.

If the removed server was the default, the default is cleared. Use --yes to skip
the confirmation prompt.

Examples:
  cobalt servers remove staging --yes
  cobalt servers remove dev`,
		Args:  cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if err := confirm(cmd, "Remove server \""+name+"\"?"); err != nil {
				return err
			}

			cpath, err := cliconfig.DefaultPath()
			if err != nil {
				return err
			}
			cfg, err := cliconfig.Load(cpath)
			if err != nil {
				return err
			}
			if _, ok := cfg.Servers[name]; !ok {
				output.Errf("server %q not found", name)
				return nil
			}
			delete(cfg.Servers, name)
			if cfg.DefaultServer == name {
				cfg.DefaultServer = ""
			}
			return cliconfig.Save(cpath, cfg)
		}),
	}
}
