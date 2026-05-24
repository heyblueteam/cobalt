package main

import (
	"fmt"
	"strings"

	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
	"github.com/spf13/cobra"
)

func newProjectsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"project"},
		Short:   "Manage projects",
	}

	cmd.AddCommand(
		newProjectsListCmd(),
		newProjectsAddCmd(),
		newProjectsRemoveCmd(),
		newProjectsRenameCmd(),
	)

	return cmd
}

func newProjectsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Long: `Lists all projects on the active server.

Shows project name, GitHub repository, and branch.

Examples:
  cobalt projects list
  cobalt projects list --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			projects, err := c.ListProjects(cmd.Context())
			if err != nil {
				return err
			}
			if len(projects) == 0 {
				output.PrintLines("No projects found.")
				return nil
			}
			if output.IsJSON() {
				output.PrintJSON(projects)
				return nil
			}
			headers := []string{"NAME", "GITHUB", "BRANCH"}
			var rows [][]string
			for _, p := range projects {
				rows = append(rows, []string{p.Name, p.GithubRepo, p.Branch})
			}
			output.PrintTable(headers, rows)
			return nil
		}),
	}
}

func newProjectsAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a new project",
		Long: `Creates a new project on the cobalt server.

Requires --github with an owner/repo pair. Optionally attach a domain at creation
time with --domain. For monorepos where one repo hosts multiple projects, use
--path to point the deploy at a sub-directory; the cobalt.json and Dockerfile
contexts are resolved relative to ` + "`<repo>/<path>`" + `.

Examples:
  cobalt projects add api --github acme/api
  cobalt projects add web --github acme/web --branch develop --domain web.example.com
  cobalt projects add api --github acme/monorepo --path api --domain api.example.com
  cobalt projects add web --github acme/monorepo --path services/web`,
		Args:  cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			srv, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			cname := args[0]
			if err := validator.ValidateProjectName(cname); err != nil {
				return err
			}
			gh := cmd.Flag("github").Value.String()
			if err := validator.ValidateGitHubRepo(gh); err != nil {
				return err
			}
			subdir := cmd.Flag("path").Value.String()
			if err := validator.ValidateProjectPath(subdir); err != nil {
				return err
			}
			req := cobaltapi.ProjectCreateRequest{
				Name:       cname,
				GithubRepo: gh,
				Branch:     cmd.Flag("branch").Value.String(),
				Path:       subdir,
				Domain:     cmd.Flag("domain").Value.String(),
			}
			cl := client.New(srv)
			project, err := cl.CreateProject(cmd.Context(), req)
			if err != nil {
				return err
			}
			output.PrintLines("Project " + project.Name + " created.")
			return nil
		}),
	}
	cmd.Flags().String("github", "", "github repo as owner/repo (required)")
	cmd.Flags().String("branch", "main", "git branch to deploy")
	cmd.Flags().String("path", "", "sub-directory inside the repo (default: repo root)")
	cmd.Flags().String("domain", "", "first domain to attach (optional)")
	return cmd
}

func newProjectsRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a project",
		Long: `Permanently deletes a project and all associated resources.

This removes domains, env vars, and deployment history. Use --yes to skip the
confirmation prompt.

Examples:
  cobalt projects remove staging-app --yes`,
		Args:  cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := confirm(cmd, "Remove project \""+name+"\"?"); err != nil {
				return err
			}
			c, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			cl := client.New(c)
			return cl.DeleteProject(cmd.Context(), name)
		}),
	}
}

func newProjectsRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <old-name> <new-name>",
		Short: "Rename a project",
		Long: `Renames a project. The project's internal ID stays the same — only the
display name changes. Use --yes to skip the confirmation prompt.

Examples:
  cobalt projects rename api api-v2 --yes`,
		Args:  cobra.ExactArgs(2),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			oldName, newName := args[0], args[1]
			if err := validator.ValidateProjectName(newName); err != nil {
				return err
			}
			replacer := strings.NewReplacer("\"", "\\\"", "\\", "\\\\")
			msg := fmt.Sprintf("Rename project \"%s\" to \"%s\"?", replacer.Replace(oldName), replacer.Replace(newName))
			if err := confirm(cmd, msg); err != nil {
				return err
			}
			c, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			cl := client.New(c)
			project, err := cl.RenameProject(cmd.Context(), oldName, cobaltapi.ProjectRenameRequest{
				Name: newName,
			})
			if err != nil {
				return err
			}
			output.PrintLines("Project renamed to " + project.Name)
			return nil
		}),
	}
}
