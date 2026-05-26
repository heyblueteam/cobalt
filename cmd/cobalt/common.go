package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/heyblueteam/cobalt/internal/cliconfig"
	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/spf13/cobra"
)

func resolveContext(cmd *cobra.Command) (cliconfig.Server, string, error) {
	srv, err := resolveServer(cmd)
	if err != nil {
		return cliconfig.Server{}, "", err
	}
	project, err := cliconfig.ResolveProject(
		cmd.Flag("project").Value.String(),
		os.Getenv("COBALT_PROJECT"),
		wd(),
		srv,
	)
	if err != nil {
		return cliconfig.Server{}, "", err
	}
	return srv, project, nil
}

func resolveServer(cmd *cobra.Command) (cliconfig.Server, error) {
	cpath, err := cliconfig.DefaultPath()
	if err != nil {
		return cliconfig.Server{}, fmt.Errorf("config path: %w", err)
	}
	cfg, err := cliconfig.Load(cpath)
	if err != nil {
		return cliconfig.Server{}, err
	}
	explicit := cmd.Flag("server").Value.String()
	if f := cmd.Flags().Lookup("server"); f != nil && !f.Changed {
		explicit = ""
	}
	name, srv, err := cfg.Active(explicit)
	if err != nil {
		return cliconfig.Server{}, err
	}
	_ = name
	return srv, nil
}

func newClient(cmd *cobra.Command) (*client.Client, error) {
	srv, err := resolveServer(cmd)
	if err != nil {
		return nil, err
	}
	return client.New(srv), nil
}

type projectClient struct {
	*client.Client
	project string
}

func (pc *projectClient) WrapProject() string { return pc.project }

func newProjectClient(cmd *cobra.Command) (*projectClient, error) {
	srv, project, err := resolveContext(cmd)
	if err != nil {
		return nil, err
	}
	return &projectClient{
		Client:  client.New(srv),
		project: project,
	}, nil
}

func setupOutput(cmd *cobra.Command) {
	jsonFlag := cmd.Flag("json").Value.String() == "true"
	output.SetJSON(jsonFlag)
	output.SetColor(!jsonFlag && output.IsStdoutTTY())
}

type confirmError struct{}

func (confirmError) Error() string { return "aborted" }

func IsConfirmAbort(err error) bool {
	var ce confirmError
	return errors.As(err, &ce)
}

// exitCodeError lets a command propagate a specific process exit code
// up to main() without calling os.Exit mid-flow (which would skip any
// deferred cleanup — terminal restore, signal stop, etc.). main()
// unwraps via errors.As and calls os.Exit AFTER all defers have run.
type exitCodeError struct{ Code int }

func (e exitCodeError) Error() string { return fmt.Sprintf("exit code %d", e.Code) }

func confirm(cmd *cobra.Command, msg string) error {
	if cmd.Flag("yes").Value.String() == "true" {
		return nil
	}
	if !output.IsTTY(os.Stdin) {
		return fmt.Errorf("stdin is not a terminal; use --yes to confirm non-interactively")
	}
	fmt.Fprintf(output.Stderr, "%s [y/N]: ", msg)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	line = strings.TrimSpace(strings.ToLower(line))
	if line != "y" && line != "yes" {
		return confirmError{}
	}
	return nil
}

func runE(fn func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		setupOutput(cmd)
		return fn(cmd, args)
	}
}

func wd() string {
	d, err := os.Getwd()
	if err != nil {
		return ""
	}
	return d
}
