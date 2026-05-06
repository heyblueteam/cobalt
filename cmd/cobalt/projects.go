package main

import (
	"fmt"
	"strings"

	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
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
		Args:  cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			srv, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			cname := args[0]
			req := cobaltapi.ProjectCreateRequest{
				Name:       cname,
				GithubRepo: cmd.Flag("github").Value.String(),
				Branch:     cmd.Flag("branch").Value.String(),
				Domain:     cmd.Flag("domain").Value.String(),
			}
			if req.GithubRepo == "" {
				return fmt.Errorf("--github is required (e.g., --github owner/repo)")
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
	cmd.Flags().String("domain", "", "first domain to attach (optional)")
	return cmd
}

func newProjectsRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a project",
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
		Args:  cobra.ExactArgs(2),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			oldName, newName := args[0], args[1]
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
