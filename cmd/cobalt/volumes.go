package main

import (
	"fmt"
	"io"
	"os"

	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/spf13/cobra"
)

func newVolumesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "volumes",
		Aliases: []string{"volume"},
		Short:   "Manage project volumes",
	}
	cmd.PersistentFlags().String("project", "", "project name")
	cmd.AddCommand(
		newVolumesListCmd(),
		newVolumesExportCmd(),
		newVolumesImportCmd(),
	)
	return cmd
}

func newVolumesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List project volumes",
		Long: `Lists named volumes for a project. Volumes outlive deployments and hold
persistent data like databases and uploads.

Examples:
  cobalt volumes list --project api
  cobalt volumes list --project api --json`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			vols, err := pc.ListVolumes(cmd.Context(), pc.WrapProject())
			if err != nil {
				return err
			}
			if output.IsJSON() {
				output.PrintJSON(vols)
				return nil
			}
			if len(vols) == 0 {
				output.PrintLines("No volumes found.")
				return nil
			}
			headers := []string{"NAME", "DOCKER NAME"}
			var rows [][]string
			for _, v := range vols {
				rows = append(rows, []string{v.Name, v.FullName})
			}
			output.PrintTable(headers, rows)
			return nil
		}),
	}
}

func newVolumesExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a volume to stdout or file",
		Long: `Exports a volume's contents as a gzipped tar to stdout or a file.

By default writes binary data to stdout — use --output to save to a file,
or --force to allow stdout when connected to a TTY.

Examples:
  cobalt volumes export --project api --volume data --output data.tar.gz
  cobalt volumes export --project api --volume data --force | tar tzf -`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			if err := requireString(cmd, "volume"); err != nil {
				return err
			}
			vol, _ := cmd.Flags().GetString("volume")
			outPath, _ := cmd.Flags().GetString("output")
			force, _ := cmd.Flags().GetBool("force")

			if outPath == "" {
				if !output.IsStdoutTTY() && !force {
					return fmt.Errorf("refusing to write binary data to terminal; use --output <path> or --force")
				}
			}

			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			resp, err := pc.ExportVolume(cmd.Context(), pc.WrapProject(), vol)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 300 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("%s: %s", resp.Status, string(body))
			}

			if outPath != "" {
				f, err := os.Create(outPath)
				if err != nil {
					return fmt.Errorf("create %q: %w", outPath, err)
				}
				defer f.Close()
				if _, err := io.Copy(f, resp.Body); err != nil {
					return fmt.Errorf("write %q: %w", outPath, err)
				}
				output.PrintLines("Exported to " + outPath)
				return nil
			}

			if _, err := io.Copy(output.Stdout, resp.Body); err != nil {
				return fmt.Errorf("write to stdout: %w", err)
			}
			return nil
		}),
	}
	cmd.Flags().String("volume", "", "volume name to export")
	cmd.Flags().String("output", "", "file path to write export to")
	cmd.Flags().Bool("force", false, "allow writing binary data to stdout even when TTY")
	return cmd
}

func newVolumesImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a volume from stdin or file",
		Long: `Imports a gzipped tar into a volume.

Reads from --input <path> or stdin. Refuses to read from a TTY stdin unless
you pipe data.

Examples:
  cobalt volumes import --project api --volume data < data.tar.gz
  cobalt volumes import --project api --volume data --input data.tar.gz`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			vol, _ := cmd.Flags().GetString("volume")
			inPath, _ := cmd.Flags().GetString("input")

			if vol == "" {
				return fmt.Errorf("--volume is required")
			}

			var data []byte
			if inPath != "" {
				var err error
				data, err = os.ReadFile(inPath)
				if err != nil {
					return fmt.Errorf("read %q: %w", inPath, err)
				}
			} else {
				if output.IsTTY(os.Stdin) {
					return fmt.Errorf("refusing to read from terminal; use --input <path> or pipe data")
				}
				var err error
				data, err = io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
			}

			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			resp, err := pc.ImportVolume(cmd.Context(), pc.WrapProject(), vol, data)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 300 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("%s: %s", resp.Status, string(body))
			}
			output.PrintLines("Imported to volume " + vol)
			return nil
		}),
	}
	cmd.Flags().String("volume", "", "volume name to import into")
	cmd.Flags().String("input", "", "file path to read import from")
	return cmd
}
