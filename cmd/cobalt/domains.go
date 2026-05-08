package main

import (
	"fmt"
	"strings"

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
		Long: `Lists all domains attached to a project. Redirects show as
"redirect → <target>" so the routing topology is visible at a
glance.

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
			headers := []string{"DOMAIN", "TYPE"}
			var rows [][]string
			for _, d := range domains {
				typeCol := domainTypeDisplay(d)
				rows = append(rows, []string{d.Name, typeCol})
			}
			output.PrintTable(headers, rows)
			return nil
		}),
	}
}

// domainTypeDisplay renders a Domain's type column for the list view.
// Older daemons may omit Type entirely on the wire; treat absent as
// primary so mixed-version installs render sensibly.
func domainTypeDisplay(d cobaltapi.Domain) string {
	switch d.Type {
	case cobaltapi.DomainTypeRedirect:
		return "redirect → " + d.RedirectTo
	default:
		return "primary"
	}
}

func newDomainsAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <domain>",
		Short: "Add a domain to a project",
		Long: `Adds a domain to a project. Caddy provisions a TLS certificate automatically.

For projects with both apex and www, pass --with-www (when adding
the apex) or --with-apex (when adding the www host) to install a
301 redirect to the primary in the same step. Cobalt does NOT do
this automatically — you opt in per-domain so daemons handling
upstream redirects (e.g. Cloudflare) aren't surprised.

Examples:
  cobalt domains add api.blue.cc --project api
  cobalt domains add blue.cc --project api --with-www
  cobalt domains add www.blue.cc --project api --with-apex`,
		Args: cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			withWWW, _ := cmd.Flags().GetBool("with-www")
			withApex, _ := cmd.Flags().GetBool("with-apex")
			if withWWW && withApex {
				return fmt.Errorf("--with-www and --with-apex are mutually exclusive")
			}
			name := args[0]
			pair, kind := apexWWWPair(name)
			if withWWW {
				if kind != "apex" {
					return fmt.Errorf("--with-www only valid for apex domains (got %q)", name)
				}
			}
			if withApex {
				if kind != "www" {
					return fmt.Errorf("--with-apex only valid for www.X domains (got %q)", name)
				}
			}

			// Primary insert.
			primary, err := pc.AddDomain(cmd.Context(), pc.WrapProject(), cobaltapi.DomainAddRequest{
				Name: name,
			})
			if err != nil {
				return err
			}
			output.PrintLines("Domain " + primary.Name + " added.")

			// Optional redirect insert.
			if withWWW || withApex {
				redirect, err := pc.AddDomain(cmd.Context(), pc.WrapProject(), cobaltapi.DomainAddRequest{
					Name:       pair,
					RedirectTo: name,
				})
				if err != nil {
					// Primary is already in. Surface a clear partial-state.
					return fmt.Errorf("primary %s added, but redirect %s → %s failed: %w",
						name, pair, name, err)
				}
				output.PrintLines("Redirect " + redirect.Name + " → " + name + " added.")
				return nil
			}

			// Helper hint when the operator added a domain that has a
			// logical pair AND the pair isn't already on the project.
			if !output.IsJSON() && pair != "" {
				existing, _ := pc.ListDomains(cmd.Context(), pc.WrapProject())
				if !domainListHas(existing, pair) {
					switch kind {
					case "apex":
						output.PrintLines("")
						output.PrintLines("Tip: redirect www." + name + " → " + name + " in one step next time:")
						output.PrintLines("       cobalt domains add " + name + " --with-www")
					case "www":
						output.PrintLines("")
						output.PrintLines("Tip: redirect " + pair + " → " + name + " in one step next time:")
						output.PrintLines("       cobalt domains add " + name + " --with-apex")
					}
				}
			}
			return nil
		}),
	}
	cmd.Flags().Bool("with-www", false, "also install a 301 redirect from www.<domain>")
	cmd.Flags().Bool("with-apex", false, "also install a 301 redirect from the apex (for www.X domains)")
	return cmd
}

func newDomainsRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <domain>",
		Short: "Remove a domain from a project",
		Long: `Removes a domain from a project. Cleans up the associated Caddy
configuration. If the removed domain is a primary that has redirects
pointing at it, those redirects are removed too. Use --yes to skip
the confirmation prompt.

Examples:
  cobalt domains remove api.blue.cc --project api --yes`,
		Args: cobra.ExactArgs(1),
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

// apexWWWPair returns the partner host and the input's role, when the
// input is an apex (`example.com`) or a www-of-apex (`www.example.com`).
// kind ∈ {"apex","www",""}; pair is "" when the input has no partner.
//
// Mirrors disco's _get_apex_www_redirect_for_domain logic: only 2-part
// apex and 3-part www get a partner; subdomains like app.example.com
// get nothing.
func apexWWWPair(domain string) (pair, kind string) {
	parts := strings.Split(domain, ".")
	if len(parts) == 2 {
		return "www." + domain, "apex"
	}
	if len(parts) == 3 && parts[0] == "www" {
		return strings.Join(parts[1:], "."), "www"
	}
	return "", ""
}

func domainListHas(domains []cobaltapi.Domain, name string) bool {
	for _, d := range domains {
		if d.Name == name {
			return true
		}
	}
	return false
}
