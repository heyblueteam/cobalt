package main

import (
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/spf13/cobra"
)

func newDomainsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "domains",
		Aliases: []string{"domain"},
		Short:   "Manage project domains",
	}
	cmd.PersistentFlags().String("project", "", "project name")
	cmd.AddCommand(
		newDomainsListCmd(),
		newDomainsAddCmd(),
		newDomainsRemoveCmd(),
	)
	return cmd
}

func newDomainsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List project domains",
		Long: `Lists all domains attached to a project.

Examples:
  cobalt domains list --project api
  cobalt domains list --project api --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			domains, err := pc.ListDomains(cmd.Context(), pc.WrapProject())
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(domains)
				return nil
			}
			if len(domains) == 0 {
				output.PrintLines("No domains configured.")
				return nil
			}
			headers := []string{"DOMAIN"}
			var rows [][]string
			for _, d := range domains {
				rows = append(rows, []string{d.Name})
			}
			output.PrintTable(headers, rows)
			return nil
		}),
	}
}

func newDomainsAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <domain>",
		Short: "Add a domain to a project",
		Long: `Adds a domain to a project. Caddy provisions a TLS certificate automatically.

Examples:
  cobalt domains add api.blue.cc --project api`,
		Args:  cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			d, err := pc.AddDomain(cmd.Context(), pc.WrapProject(), cobaltapi.DomainAddRequest{
				Name: args[0],
			})
			if err != nil {
				return err
			}
			output.PrintLines("Domain " + d.Name + " added.")
			return nil
		}),
	}
}

func newDomainsRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <domain>",
		Short: "Remove a domain from a project",
		Long: `Removes a domain from a project. Cleans up the associated Caddy configuration.
Use --yes to skip the confirmation prompt.

Examples:
  cobalt domains remove api.blue.cc --project api --yes`,
		Args:  cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			domain := args[0]
			if err := confirm(cmd, "Remove domain \""+domain+"\"?"); err != nil {
				return err
			}
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			return pc.RemoveDomain(cmd.Context(), pc.WrapProject(), domain)
		}),
	}
}
