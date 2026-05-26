package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

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

By default the daemon allocates a real PTY when both stdin and stdout
are terminals; pass --no-tty to force pipe semantics for scripted use,
or --tty to force PTY when one side isn't a terminal.`,
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("command is required, e.g. 'cobalt run --project api -- ls -la'")
			}
			command := joinRunArgs(args)
			service, _ := cmd.Flags().GetString("service")

			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			ttyMode := resolveTTYMode(cmd)
			conn, subprotocol, err := pc.RunWS(ctx, pc.WrapProject(), command, service, ttyMode)
			if err != nil {
				return err
			}
			defer conn.Close(websocket.StatusNormalClosure, "done")

			switch subprotocol {
			case cobaltapi.RunSubprotocolV2:
				return runClientV2(ctx, cancel, conn, ttyMode)
			default:
				// v1 fallback. ttyMode is ignored on the wire (v1 has no
				// resize/PTY support) but we still honour it for raw
				// stdin handling.
				return runClientV1(ctx, cancel, conn, ttyMode)
			}
		}),
	}
	cmd.Flags().String("project", "", "project name")
	cmd.Flags().String("service", "web", "service name (defaults to web)")
	cmd.Flags().Bool("tty", false, "force allocate a PTY even when stdio isn't a terminal")
	cmd.Flags().Bool("no-tty", false, "force no PTY even when stdio is a terminal")
	return cmd
}

// joinRunArgs assembles the user's command from cobra's split argv.
// `cobalt run -- echo hello` arrives as []string{"echo","hello"}; the
// daemon wraps the result in `sh -c "..."`, so joining with a space
// reconstitutes the line the user typed (modulo any quoting cobra
// already stripped). A single quoted arg (`cobalt run -- "ls -la"`)
// passes through unchanged.
func joinRunArgs(args []string) string {
	return strings.Join(args, " ")
}

// resolveTTYMode picks PTY vs pipes. --tty / --no-tty override; the
// default is auto: PTY when both stdin and stdout are real terminals.
func resolveTTYMode(cmd *cobra.Command) bool {
	forceTTY, _ := cmd.Flags().GetBool("tty")
	noTTY, _ := cmd.Flags().GetBool("no-tty")
	if forceTTY {
		return true
	}
	if noTTY {
		return false
	}
	return output.IsTTY(os.Stdin) && output.IsTTY(os.Stdout)
}

// runClientV2 implements the kubectl-style binary multiplexed
// protocol. See plans/cobalt/cobalt-run-v2.md for the channel layout.
func runClientV2(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, tty bool) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	if tty && output.IsTTY(os.Stdin) {
		oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err == nil {
			defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()
		}
	}

	// One writer goroutine: serializes WS writes (coder/websocket
	// only allows one concurrent writer).
	writeCh := make(chan []byte, 32)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for frame := range writeCh {
			if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
				return
			}
		}
	}()

	// Stdin pump (fire-and-forget — once stdin EOFs we send a
	// close-stdin frame; we don't block the main flow waiting for it).
	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				frame := make([]byte, 1+n)
				frame[0] = cobaltapi.RunChannelStdin
				copy(frame[1:], buf[:n])
				select {
				case writeCh <- frame:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				// Stdin done — tell the server it's safe to close
				// its writer half. Don't cancel the context: the
				// container can still produce output.
				select {
				case writeCh <- []byte{cobaltapi.RunChannelCloseStdin}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	// Resize ticker (TTY mode only). Polling every 250 ms picks up
	// terminal resizes without wiring SIGWINCH explicitly.
	if tty && output.IsTTY(os.Stdout) {
		go runResizeLoop(ctx, writeCh)
	}

	// Read loop on the WS — runs until exit frame, error, or close.
	exitCh := make(chan int, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			mt, payload, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if mt != websocket.MessageBinary || len(payload) < 1 {
				continue
			}
			ch, body := payload[0], payload[1:]
			switch ch {
			case cobaltapi.RunChannelStdout:
				_, _ = os.Stdout.Write(body)
			case cobaltapi.RunChannelStderr:
				_, _ = os.Stderr.Write(body)
			case cobaltapi.RunChannelExit:
				var p cobaltapi.RunExitPayload
				_ = json.Unmarshal(body, &p)
				select {
				case exitCh <- p.Code:
				default:
				}
				return
			case cobaltapi.RunChannelError:
				fmt.Fprintln(os.Stderr, "cobalt run:", string(body))
				return
			}
		}
	}()

	select {
	case code := <-exitCh:
		cancel()
		close(writeCh)
		<-writerDone
		if code != 0 {
			// Propagate as an error so deferred cleanup (terminal
			// restore, signal stop) runs before main() exits.
			return exitCodeError{Code: code}
		}
		return nil
	case <-sigCh:
		cancel()
		close(writeCh)
		<-writerDone
		<-readDone
		return nil
	case <-readDone:
		// WS ended without an exit frame; treat as success.
		cancel()
		close(writeCh)
		<-writerDone
		return nil
	case <-ctx.Done():
		close(writeCh)
		<-writerDone
		<-readDone
		return nil
	}
}

// runResizeLoop polls the local terminal's size every 250 ms and
// emits channel-4 frames whenever it changes. A simpler poll-based
// approach than wiring SIGWINCH; the cost is one tiny frame per
// resize event with up-to-250-ms latency.
func runResizeLoop(ctx context.Context, writeCh chan<- []byte) {
	var lastRows, lastCols uint16
	send := func() {
		w, h, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil {
			return
		}
		// Terminal cell counts safely fit in uint16 (real-world dims
		// are bounded by display hardware; values >65535 are not a
		// thing).
		rows, cols := uint16(h), uint16(w) //nolint:gosec // bounded by terminal size
		if rows == lastRows && cols == lastCols {
			return
		}
		lastRows, lastCols = rows, cols
		body, _ := json.Marshal(cobaltapi.RunResizePayload{Rows: rows, Cols: cols})
		frame := make([]byte, 1+len(body))
		frame[0] = cobaltapi.RunChannelResize
		copy(frame[1:], body)
		select {
		case writeCh <- frame:
		case <-ctx.Done():
		}
	}
	send() // emit the initial size right away
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			send()
		}
	}
}

// runClientV1 is the legacy JSON-text-frame client, kept for the case
// where a (very) old server only advertises cobalt-run.v1.
func runClientV1(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, ttyMode bool) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	exitCh := make(chan int, 1)
	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		runV1ReadLoop(ctx, conn, exitCh)
	}()

	if ttyMode && output.IsTTY(os.Stdin) {
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
			if n > 0 {
				frame := cobaltapi.RunFrame{
					Type: cobaltapi.RunFrameStdin,
					Data: string(buf[:n]),
				}
				b, _ := json.Marshal(frame)
				if writeErr := conn.Write(ctx, websocket.MessageText, b); writeErr != nil {
					cancel()
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	select {
	case code := <-exitCh:
		cancel()
		if code != 0 {
			// Propagate as an error so deferred cleanup (terminal
			// restore, signal stop) runs before main() exits.
			return exitCodeError{Code: code}
		}
		return nil
	case <-sigCh:
		cancel()
		<-doneCh
		return nil
	case <-doneCh:
		cancel()
		return nil
	case <-ctx.Done():
		<-doneCh
		return nil
	}
}

func runV1ReadLoop(ctx context.Context, conn *websocket.Conn, exitCh chan<- int) {
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
