package main

import (
	"fmt"
	"io"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Stream project service logs",
		Long: `Streams logs from a project's running service over SSE.

Follows logs in real time (like 'tail -f'). Use --service to select which
service to tail (defaults to "web"). Press Ctrl+C to stop.

Examples:
  cobalt logs --project api
  cobalt logs --project api --service worker`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			service, _ := cmd.Flags().GetString("service")
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			resp, err := pc.LogsSSE(ctx, pc.WrapProject(), service)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("%s: %s", resp.Status, string(body))
			}

			return output.ConsumeSSE(ctx, resp.Body, output.Stdout)
		}),
	}
	cmd.Flags().String("project", "", "project name")
	cmd.Flags().String("service", "", "service name (defaults to web)")
	return cmd
}
