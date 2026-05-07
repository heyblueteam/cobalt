package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

func requireString(cmd *cobra.Command, name string) error {
	if val, _ := cmd.Flags().GetString(name); val == "" {
		return fmt.Errorf("--%s is required", name)
	}
	return nil
}

func requirePositiveInt(cmd *cobra.Command, name string) (int64, error) {
	s, _ := cmd.Flags().GetString(name)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("--%s must be a non-negative integer", name)
	}
	return n, nil
}