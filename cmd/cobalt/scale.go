package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/spf13/cobra"
)

func newScaleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scale",
		Short: "Manage service replica counts",
	}
	cmd.PersistentFlags().String("project", "", "project name")
	cmd.AddCommand(
		newScaleListCmd(),
		newScaleSetCmd(),
	)
	return cmd
}

func newScaleListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show current replicas per service",
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			info, err := pc.GetScale(cmd.Context(), pc.WrapProject())
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(info)
				return nil
			}
			if len(info.Services) == 0 {
				output.PrintLines("No services running.")
				return nil
			}
			headers := []string{"SERVICE", "REPLICAS"}
			var rows [][]string
			for _, s := range info.Services {
				rows = append(rows, []string{s.Name, strconv.Itoa(s.Replicas)})
			}
			output.PrintTable(headers, rows)
			return nil
		}),
	}
}

func newScaleSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set <SERVICE=N> [SERVICE=N ...]",
		Aliases: []string{"apply"},
		Short:   "Set replica counts for services",
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("at least one SERVICE=N argument is required")
			}
			var services []cobaltapi.ScaleService
			for _, arg := range args {
				parts := strings.SplitN(arg, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid argument: %q — expected SERVICE=N", arg)
				}
				n, err := strconv.Atoi(parts[1])
				if err != nil {
					return fmt.Errorf("invalid replica count %q: %w", parts[1], err)
				}
				services = append(services, cobaltapi.ScaleService{
					Name:     parts[0],
					Replicas: n,
				})
			}
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			info, err := pc.SetScale(cmd.Context(), pc.WrapProject(), cobaltapi.ScaleSetRequest{
				Services: services,
			})
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(info)
				return nil
			}
			for _, s := range info.Services {
				output.PrintLines(fmt.Sprintf("%s=%d", s.Name, s.Replicas))
			}
			return nil
		}),
	}
}
