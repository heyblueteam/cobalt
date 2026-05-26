package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func requireString(cmd *cobra.Command, name string) error {
	if val, _ := cmd.Flags().GetString(name); val == "" {
		return fmt.Errorf("--%s is required", name)
	}
	return nil
}
