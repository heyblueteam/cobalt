package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type InstallTarget string

const (
	TargetClaude InstallTarget = "claude"
	TargetCodex  InstallTarget = "codex"
)

type InstallTargetOrAll interface{}

func ExpandTargets(t InstallTargetOrAll) []InstallTarget {
	switch v := t.(type) {
	case string:
		if v == "all" {
			return []InstallTarget{TargetClaude, TargetCodex}
		}
		return []InstallTarget{InstallTarget(v)}
	default:
		return nil
	}
}

func InstallPath(t InstallTarget, home string) string {
	switch t {
	case TargetClaude:
		return filepath.Join(home, ".claude", "skills", "cobalt", "SKILL.md")
	case TargetCodex:
		return filepath.Join(home, ".codex", "skills", "cobalt", "SKILL.md")
	default:
		return ""
	}
}

func RenderFrontmatter(name string) string {
	return fmt.Sprintf(`---
description: %s agent skill for Cobalt CLI
---
`, name)
}

func RenderBundle(commands []*cobra.Command) string {
	return NARRATIVE + "\n\n" + renderCommands(commands) + "\n"
}

func EnsureDir(path string) error {
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0o755)
}

func renderCommands(commands []*cobra.Command) string {
	var lines []string
	lines = append(lines, "## CLI Command Reference\n")

	var allCmds []*cobra.Command
	for _, cmd := range commands {
		if cmd.Name() == "llm" || cmd.Name() == "help" {
			continue
		}
		allCmds = append(allCmds, cmd)
		allCmds = append(allCmds, flattenSubcommands(cmd)...)
	}

	sort.Slice(allCmds, func(i, j int) bool {
		return allCmds[i].Name() < allCmds[j].Name()
	})

	seen := make(map[string]bool)
	for _, cmd := range allCmds {
		if seen[cmd.Name()] {
			continue
		}
		seen[cmd.Name()] = true
		lines = append(lines, renderCommand(cmd))
	}

	return strings.Join(lines, "\n")
}

func flattenSubcommands(cmd *cobra.Command) []*cobra.Command {
	var cmds []*cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Hidden {
			continue
		}
		cmds = append(cmds, sub)
		cmds = append(cmds, flattenSubcommands(sub)...)
	}
	return cmds
}

func renderCommand(cmd *cobra.Command) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("### %s", cmd.Name()))

	if cmd.Short != "" {
		lines = append(lines, cmd.Short)
		lines = append(lines, "")
	}

	if cmd.Long != "" {
		lines = append(lines, cmd.Long)
		lines = append(lines, "")
	}

	lines = append(lines, "```bash")
	lines = append(lines, formatUsage(cmd))
	lines = append(lines, "```")
	lines = append(lines, "")

	if cmd.Example != "" {
		lines = append(lines, "Examples:")
		lines = append(lines, "```bash")
		for _, example := range strings.Split(cmd.Example, "\n") {
			if strings.TrimSpace(example) != "" {
				lines = append(lines, example)
			}
		}
		lines = append(lines, "```")
		lines = append(lines, "")
	}

	if cmd.Flags().HasFlags() {
		lines = append(lines, "Flags:")
		cmd.Flags().VisitAll(func(flag *pflag.Flag) {
			desc := flag.Usage
			if flag.DefValue != "" {
				desc = desc + fmt.Sprintf(" (default: %s)", flag.DefValue)
			}
			lines = append(lines, fmt.Sprintf("  --%s %s: %s", flag.Name, flag.Value.Type(), desc))
		})
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

func formatUsage(cmd *cobra.Command) string {
	useLine := cmd.UseLine()
	// UseLine includes the root command name, strip it if present
	useLine = strings.TrimPrefix(useLine, "cobalt ")
	return "cobalt " + useLine
}
