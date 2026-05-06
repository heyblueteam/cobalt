package main

import (
	"os/signal"
	"syscall"

	"github.com/heyblueteam/cobalt/internal/server"
	"github.com/spf13/cobra"
)

func newServerCmd() *cobra.Command {
	var cfg server.Config

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the cobalt daemon",
		Long: `Starts the cobalt HTTP API server.

This is the daemon side of cobalt. It is normally started by Docker (or systemd)
on a host where cobalt manages deployments. End users interact via other
subcommands of the same binary, not by running 'cobalt server' directly.`,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(c.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return server.Run(ctx, cfg)
		},
	}

	cmd.Flags().StringVar(&cfg.Addr, "addr", ":80", "HTTP listen address")
	cmd.Flags().StringVar(&cfg.DataDir, "data-dir", "/cobalt/data", "directory for sqlite db and project state")
	cmd.Flags().StringVar(&cfg.CaddySocket, "caddy-socket", "", "Caddy admin unix socket path (empty = default /cobalt/caddy-socket/caddy.sock)")

	return cmd
}
