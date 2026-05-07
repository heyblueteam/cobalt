package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/heyblueteam/cobalt/internal/llm"
	"github.com/spf13/cobra"
)

func newLlmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "llm",
		Short: "Print a markdown bundle for LLM coding agents",
		Long: `Prints a markdown bundle (narrative + CLI command reference) suitable
for piping into an LLM coding agent's skill file.

Examples:
  cobalt llm                       # print to stdout
  cobalt llm --save                # write ./COBALT.md
  cobalt llm --install claude      # install as Claude agent skill
  cobalt llm --install codex       # install as Codex agent skill
  cobalt llm --install all          # install for both agents
  cobalt llm --install claude --force  # overwrite existing`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			target, _ := cmd.Flags().GetString("install")
			save, _ := cmd.Flags().GetBool("save")
			force, _ := cmd.Flags().GetBool("force")

			commands := flattenCommands(cmd)

			if target != "" {
				targets := llm.ExpandTargets(target)
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("get home dir: %w", err)
				}

				body := llm.RenderBundle(commands)

				if !force {
					for _, t := range targets {
						path := llm.InstallPath(t, home)
						if _, err := os.Stat(path); err == nil {
							return fmt.Errorf("file already exists: %s\n\nPass --force to overwrite", path)
						}
					}
				}

				content := llm.RenderFrontmatter("Claude") + body
				for _, t := range targets {
					path := llm.InstallPath(t, home)
					if err := llm.EnsureDir(path); err != nil {
						return err
					}
					if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
						return err
					}
					fmt.Printf("Installed %s skill: %s\n", t, path)
				}
				return nil
			}

			if save {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				path := filepath.Join(cwd, "COBALT.md")
				body := llm.RenderBundle(commands)
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					return err
				}
				fmt.Printf("Wrote %s\n", path)
				return nil
			}

			fmt.Print(llm.RenderBundle(commands))
			return nil
		}),
	}

	cmd.Flags().Bool("save", false, "write the markdown bundle to ./COBALT.md")
	cmd.Flags().String("install", "", "install as an agent skill (claude, codex, or all)")
	cmd.Flags().Bool("force", false, "overwrite existing files when installing")
	return cmd
}

func flattenCommands(cmd *cobra.Command) []*cobra.Command {
	var cmds []*cobra.Command
	for _, c := range cmd.Parent().Commands() {
		if c.Name() == "llm" || c.Name() == "help" {
			continue
		}
		cmds = append(cmds, flattenSubcommands(c)...)
	}
	return cmds
}

func flattenSubcommands(cmd *cobra.Command) []*cobra.Command {
	var cmds []*cobra.Command
	if !cmd.Hidden {
		cmds = append(cmds, cmd)
	}
	for _, sub := range cmd.Commands() {
		if !sub.Hidden {
			cmds = append(cmds, flattenSubcommands(sub)...)
		}
	}
	return cmds
}
