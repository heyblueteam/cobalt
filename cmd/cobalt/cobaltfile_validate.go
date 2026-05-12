package main

import (
	"fmt"
	"os"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/spf13/cobra"
)

// newValidateCmd returns the `cobalt validate` subcommand: local
// cobalt.json parsing + validation, no daemon required. Useful as a
// pre-commit hook and inside CI before a push triggers a deploy.
//
// Lives in cobaltfile_validate.go (not validate.go) because the latter
// already holds unrelated flag-validation helpers.
func newValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate a cobalt.json file (defaults to ./cobalt.json)",
		Long: `Parses and validates a cobalt.json file locally, without contacting
the daemon. Runs the same parser the daemon uses on deploy — applies
defaults, then enforces enum values, hook-service shape, cron schedule
shape, port ranges, etc.

Exits 0 on success with a one-line summary, non-zero on any error.

Examples:
  cobalt validate
  cobalt validate path/to/cobalt.json
  cobalt validate --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			path := "cobalt.json"
			if len(args) == 1 {
				path = args[0]
			}
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			cf, err := cobaltfile.ParseFile(path)
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(map[string]any{
					"path":     path,
					"version":  cf.Version,
					"name":     cf.Name,
					"services": len(cf.Services),
					"images":   len(cf.Images),
					"valid":    true,
				})
				return nil
			}
			output.PrintLines(fmt.Sprintf("✅ %s is valid (%d services, %d images)",
				path, len(cf.Services), len(cf.Images)))
			return nil
		}),
	}
	return cmd
}
