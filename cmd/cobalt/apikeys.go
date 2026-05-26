package main

import (
	"fmt"
	"strconv"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/spf13/cobra"
)

func newApikeysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "apikeys",
		Aliases: []string{"apikey"},
		Short:   "Manage API keys",
	}
	cmd.AddCommand(
		newApikeysListCmd(),
		newApikeysCreateCmd(),
		newApikeysRemoveCmd(),
	)
	return cmd
}

func newApikeysListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List API keys",
		Long: `Lists all API keys. Raw key values are never returned — they are only
available at creation time.

Examples:
  cobalt apikeys list
  cobalt apikeys list --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			keys, err := c.ListAPIKeys(cmd.Context())
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(keys)
				return nil
			}
			if len(keys) == 0 {
				output.PrintLines("No API keys found.")
				return nil
			}
			headers := []string{"ID", "NAME", "CREATED"}
			var rows [][]string
			for _, k := range keys {
				rows = append(rows, []string{
					strconv.FormatInt(k.ID, 10),
					k.Name,
					fmt.Sprintf("%d", k.CreatedAt),
				})
			}
			output.PrintTable(headers, rows)
			return nil
		}),
	}
}

func newApikeysCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new API key",
		Long: `Creates a new API key. The raw key is printed to stdout once — save it
immediately because cobalt only stores the hash and cannot recover it.

Examples:
  cobalt apikeys create prod-key
  cobalt apikeys create prod-key | pbcopy`,
		Args: cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			resp, err := c.CreateAPIKey(cmd.Context(), cobaltapi.APIKeyCreateRequest{
				Name: args[0],
			})
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(resp)
				return nil
			}
			output.Errf("Key created. Save this — it cannot be recovered.")
			output.PrintLines(resp.Key)
			return nil
		}),
	}
}

func newApikeysRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove an API key",
		Long: `Deletes an API key by ID. Subsequent requests using this key will return 401.
Use --yes to skip the confirmation prompt.

Examples:
  cobalt apikeys remove 1 --yes`,
		Args: cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			if err := confirm(cmd, "Remove API key "+args[0]+"?"); err != nil {
				return err
			}
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid id: %q", args[0])
			}
			c, err2 := newClient(cmd)
			if err2 != nil {
				return err2
			}
			return c.DeleteAPIKey(cmd.Context(), id)
		}),
	}
}
