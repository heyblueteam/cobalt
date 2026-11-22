package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/spf13/cobra"
)

func newRegistriesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "registries",
		Aliases: []string{"registry"},
		Short:   "Manage registry logins for prebuilt image pulls",
		Long: `Registry logins let the daemon pull private prebuilt images for
'cobalt deploy --image'. One login per registry host, shared by every
project. Passwords are encrypted at rest and never returned.`,
	}
	cmd.AddCommand(newRegistriesListCmd(), newRegistriesSetCmd(), newRegistriesRemoveCmd())
	return cmd
}

func newRegistriesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registry logins (no passwords)",
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			creds, err := c.ListRegistries(cmd.Context())
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(creds)
				return nil
			}
			if len(creds) == 0 {
				output.PrintLines("No registry logins.")
				return nil
			}
			var rows [][]string
			for _, r := range creds {
				rows = append(rows, []string{r.Registry, r.Username, time.Unix(r.UpdatedAt, 0).UTC().Format(time.RFC3339)})
			}
			output.PrintTable([]string{"REGISTRY", "USERNAME", "UPDATED"}, rows)
			return nil
		}),
	}
}

func newRegistriesSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <registry>",
		Short: "Add or replace the pull login for a registry host",
		Long: `Stores the login the daemon uses to pull images from <registry>. The
password (or token) is read from stdin, so it stays out of shell history
and process lists. Use a read-only token.

Examples:
  printf '%s' "$GHCR_READ_TOKEN" | cobalt registries set ghcr.io --username heyblue-bot --password-stdin`,
		Args: cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			username, _ := cmd.Flags().GetString("username")
			fromStdin, _ := cmd.Flags().GetBool("password-stdin")
			if username == "" {
				return errors.New("--username is required")
			}
			if !fromStdin {
				return errors.New("--password-stdin is required; pipe the token on stdin")
			}
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil && line == "" {
				return fmt.Errorf("read password from stdin: %w", err)
			}
			password := strings.TrimRight(line, "\r\n")
			if password == "" {
				return errors.New("empty password on stdin")
			}
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			cred, err := c.SetRegistry(cmd.Context(), args[0], cobaltapi.RegistryCredentialSetRequest{Username: username, Password: password})
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(cred)
				return nil
			}
			output.PrintLines(fmt.Sprintf("Registry login saved for %s (user %s)", cred.Registry, cred.Username))
			return nil
		}),
	}
	cmd.Flags().String("username", "", "registry username")
	cmd.Flags().Bool("password-stdin", false, "read the password or token from stdin")
	return cmd
}

func newRegistriesRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <registry>",
		Short: "Remove the pull login for a registry host",
		Args:  cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			if err := confirm(cmd, "Remove registry login for "+args[0]+"?"); err != nil {
				return err
			}
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			return c.DeleteRegistry(cmd.Context(), args[0])
		}),
	}
}
