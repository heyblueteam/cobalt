package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"

	"github.com/coder/websocket"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <command>",
		Short: "Run a one-off command in a project container",
		Long: `Runs a command in a one-shot container on the project's network.
Forwards stdin/stdout/stderr over WebSocket.`,
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("command is required, e.g. 'cobalt run --project api \"ls -la\"'")
			}
			command := args[0]
			service, _ := cmd.Flags().GetString("service")

			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			conn, err := pc.RunWS(ctx, pc.WrapProject(), command, service)
			if err != nil {
				return err
			}
			defer conn.Close(websocket.StatusNormalClosure, "done")

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt)
			defer signal.Stop(sigCh)

			exitCh := make(chan int, 1)
			doneCh := make(chan struct{})

			go func() {
				defer close(doneCh)
				runWSReadLoop(ctx, conn, exitCh)
			}()

			if output.IsTTY(os.Stdin) {
				oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
				if err == nil {
					defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()
				}
			}

			go func() {
				buf := make([]byte, 4096)
				for {
					select {
					case <-ctx.Done():
						return
					default:
					}
					n, err := os.Stdin.Read(buf)
					if err != nil {
						cancel()
						return
					}
					if n > 0 {
						frame := cobaltapi.RunFrame{
							Type: cobaltapi.RunFrameStdin,
							Data: string(buf[:n]),
						}
						b, _ := json.Marshal(frame)
						if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
							cancel()
							return
						}
					}
				}
			}()

			select {
			case code := <-exitCh:
				cancel()
				if code != 0 {
					output.Errf("command exited with code %d", code)
					os.Exit(code)
				}
				return nil
			case <-sigCh:
				cancel()
				<-doneCh
				return nil
			case <-ctx.Done():
				<-doneCh
				return nil
			}
		}),
	}
	cmd.Flags().String("project", "", "project name")
	cmd.Flags().String("service", "web", "service name (defaults to web)")
	return cmd
}

func runWSReadLoop(ctx context.Context, conn *websocket.Conn, exitCh chan<- int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, msg, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var frame cobaltapi.RunFrame
		if err := json.Unmarshal(msg, &frame); err != nil {
			continue
		}
		switch frame.Type {
		case cobaltapi.RunFrameStdout:
			fmt.Fprint(output.Stdout, frame.Data)
		case cobaltapi.RunFrameStderr:
			fmt.Fprint(output.Stderr, frame.Data)
		case cobaltapi.RunFrameExit:
			exitCh <- frame.Code
			return
		}
	}
}
