package main

import (
	"fmt"
	"strconv"

	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/spf13/cobra"
)

func newGithubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Manage GitHub App integrations",
	}
	cmd.AddCommand(
		newGithubAppsCmd(),
		newGithubReposCmd(),
	)
	return cmd
}

func newGithubAppsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "Manage GitHub Apps",
	}
	cmd.AddCommand(
		newGithubAppsListCmd(),
		newGithubAppsAddCmd(),
		newGithubAppsManageCmd(),
		newGithubAppsPruneCmd(),
	)
	return cmd
}

func newGithubAppsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered GitHub Apps",
		Long: `Lists all GitHub Apps registered with the cobalt server.

Examples:
  cobalt github apps list
  cobalt github apps list --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			apps, err := c.ListApps(cmd.Context())
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(apps)
				return nil
			}
			if len(apps) == 0 {
				output.PrintLines("No GitHub Apps registered.")
				return nil
			}
			headers := []string{"ID", "NAME", "OWNER", "URL"}
			var rows [][]string
			for _, a := range apps {
				rows = append(rows, []string{
					strconv.FormatInt(a.ID, 10),
					a.Slug,
					a.Owner,
					a.HTMLURL,
				})
			}
			output.PrintTable(headers, rows)
			return nil
		}),
	}
}

func newGithubAppsAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Register a new GitHub App",
		Long: `Starts the GitHub App registration flow.

Opens a browser to GitHub's App installation page unless --non-interactive is set.
The printed URL can be shared with an org admin to complete installation.

Examples:
  cobalt github apps add --organization heyblueteam
  cobalt github apps add --organization heyblueteam --non-interactive`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			org, _ := cmd.Flags().GetString("organization")
			nonInteractive, _ := cmd.Flags().GetBool("non-interactive")

			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			resp, err := c.CreatePendingApp(cmd.Context(), cobaltapi.PendingAppCreateRequest{
				Organization: org,
			})
			if err != nil {
				return err
			}

			output.PrintLines("URL: " + resp.URL)

			if nonInteractive {
				return nil
			}
			if err := client.OpenBrowser(resp.URL); err != nil {
				output.Errf("could not open browser: %v", err)
			}
			return nil
		}),
		Args: cobra.NoArgs,
	}
	cmd.Flags().String("organization", "", "GitHub organization name")
	cmd.Flags().Bool("non-interactive", false, "print URL and exit without opening browser")
	return cmd
}

func newGithubAppsManageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "manage <organization>",
		Short: "Open GitHub App settings in browser",
		Long: `Opens the GitHub App settings page for the given organization in a browser.

Examples:
  cobalt github apps manage heyblueteam`,
		Args:  cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			app, err := c.FindAppByOwner(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if app.HTMLURL == "" {
				return fmt.Errorf("no settings URL for app %d (was it registered via the manifest flow?)", app.ID)
			}
			output.PrintLines(app.HTMLURL)
			if err := client.OpenBrowser(app.HTMLURL); err != nil {
				output.Errf("could not open browser: %v", err)
			}
			return nil
		}),
	}
}

func newGithubAppsPruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Remove stale GitHub Apps and refresh repo list",
		Long: `Cross-references cobalt's local DB with GitHub's current state.

Removes apps and installations that no longer exist on GitHub, and adds repos
from installations that were previously missing.

Examples:
  cobalt github apps prune`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			resp, err := c.PruneApps(cmd.Context())
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(resp)
				return nil
			}
			output.PrintKeyValue(
				[2]string{"Apps removed", strconv.FormatInt(int64(resp.AppsRemoved), 10)},
				[2]string{"Installations removed", strconv.FormatInt(int64(resp.InstallationsRemoved), 10)},
				[2]string{"Repos added", strconv.FormatInt(int64(resp.ReposAdded), 10)},
				[2]string{"Repos removed", strconv.FormatInt(int64(resp.ReposRemoved), 10)},
			)
			return nil
		}),
	}
}

func newGithubReposCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "repos",
		Short: "Manage GitHub repos",
		Long: `Lists all repos accessible to cobalt's GitHub App installations.

Shows installation ID, full repo name, and default branch.

Examples:
  cobalt github repos
  cobalt github repos --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			repos, err := c.ListRepos(cmd.Context())
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(repos)
				return nil
			}
			if len(repos) == 0 {
				output.PrintLines("No repos found.")
				return nil
			}
			headers := []string{"INSTALLATION ID", "FULL NAME", "DEFAULT BRANCH"}
			var rows [][]string
			for _, r := range repos {
				rows = append(rows, []string{
					strconv.FormatInt(r.InstallationID, 10),
					r.FullName,
					r.DefaultBranch,
				})
			}
			output.PrintTable(headers, rows)
			return nil
		}),
	}
}
