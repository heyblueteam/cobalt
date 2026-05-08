package api

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// runV2 implements the cobalt-run.v2 protocol — kubectl-style
// channel-prefixed binary frames over WebSocket. See
// plans/cobalt/cobalt-run-v2.md for the wire-format spec.
//
// In TTY mode (?tty=1) we allocate a real PTY and bind the child's
// stdio to its slave end; the master is bridged to channels 0/1 of
// the WS, and channel-4 resize frames translate to TIOCSWINSZ on the
// master via pty.Setsize. In no-TTY mode we use os.Pipe() pairs so
// stdout/stderr remain separable on the wire (channels 1 and 2).
//
// os.Pipe() (vs the io.Pipe of the v1 path) is what makes this
// protocol deadlock-free: exec.Cmd.Stdin sees a real *os.File and
// dup2's it directly into the child instead of spawning an internal
// io.Copy goroutine that gates Wait() on stdin draining.
func (h *Handler) runV2(ctx context.Context, conn *websocket.Conn, req runRequest, tty bool) int {
	runCtx, cancel := newRunLifecycle(ctx, conn)
	defer cancel()

	carriers, err := newRunV2Carriers(tty)
	if err != nil {
		h.Log.Error("run: open carriers", "error", err)
		sendV2Error(runCtx, conn, "open stdio: "+err.Error())
		return -1
	}
	// Best-effort cleanup. Most fds get closed earlier in the happy
	// path; this catches the panic / early-return cases.
	defer carriers.close()

	// Single WS write goroutine. coder/websocket allows only one
	// concurrent writer; pumpers push pre-framed []byte into writeCh
	// and we serialize them here.
	writeCh := make(chan []byte, 64)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for frame := range writeCh {
			wctx, wcancel := context.WithTimeout(runCtx, runWriteTimeout)
			err := conn.Write(wctx, websocket.MessageBinary, frame)
			wcancel()
			if err != nil {
				// Drain remaining frames so producers don't block on
				// channel-send forever; we're going to close anyway.
				for range writeCh {
				}
				return
			}
		}
	}()

	// stdoutR is the read-end of stdout (or PTY master in TTY mode);
	// stderrR is non-nil only in no-TTY mode.
	var pumpWG sync.WaitGroup
	pumpWG.Add(1)
	go pumpV2(&pumpWG, carriers.stdoutR, writeCh, cobaltapi.RunChannelStdout)
	if !tty {
		pumpWG.Add(1)
		go pumpV2(&pumpWG, carriers.stderrR, writeCh, cobaltapi.RunChannelStderr)
	}

	// WS-read pump (dispatches incoming frames). Returns when the WS
	// errors (typically when we close it after the container exits).
	go func() {
		for {
			mt, payload, err := conn.Read(runCtx)
			if err != nil {
				return
			}
			if mt != websocket.MessageBinary || len(payload) < 1 {
				continue
			}
			ch := payload[0]
			body := payload[1:]
			switch ch {
			case cobaltapi.RunChannelStdin:
				if carriers.stdinW != nil {
					_, _ = carriers.stdinW.Write(body)
				}
			case cobaltapi.RunChannelCloseStdin:
				if carriers.stdinW != nil {
					_ = carriers.stdinW.Close()
					carriers.stdinW = nil
				}
			case cobaltapi.RunChannelResize:
				if !tty || carriers.ptmx == nil {
					continue
				}
				var p cobaltapi.RunResizePayload
				if err := json.Unmarshal(body, &p); err != nil {
					continue
				}
				_ = pty.Setsize(carriers.ptmx, &pty.Winsize{
					Rows: p.Rows,
					Cols: p.Cols,
				})
			default:
				// Unknown channel; ignore per spec (forward-compat).
			}
		}
	}()

	runOpts := docker.RunOpts{
		ProjectID:        req.project.ID,
		ProjectName:      req.project.Name,
		ServiceName:      req.serviceName,
		DeploymentNumber: req.live.Number,
		ContainerName:    docker.RunContainerName(req.project.Name, time.Now().UnixNano()),
		Image:            req.imageTag,
		Command:          []string{"sh", "-c", req.command},
		Networks:         []string{req.deploymentNetwork, deploy.MainNetworkName},
		Volumes:          req.volumes,
		ExtraParams:      req.extraParams,
		Stdin:            carriers.childStdin,
		Stdout:           carriers.childStdout,
		Stderr:           carriers.childStderr,
		TTY:              tty,
	}
	runErr := h.Docker.Run(runCtx, runOpts)

	// Make the pumps see EOF so they exit. In TTY mode that means
	// closing the master (slave-side close from container exit
	// produces EIO on master reads — close is the canonical
	// shutdown). In no-TTY mode, close the parent's writer halves
	// so pumpers reading the read halves observe EOF.
	carriers.closeForPumpDrain()
	pumpWG.Wait()

	exitCode := docker.ExitCode(runErr)
	if runErr != nil {
		h.Log.Info("run: container exited non-zero",
			"project", req.project.Name, "service", req.serviceName,
			"exit_code", exitCode, "error", runErr)
	}
	exitPayload, _ := json.Marshal(cobaltapi.RunExitPayload{Code: exitCode})
	select {
	case writeCh <- prependChannel(cobaltapi.RunChannelExit, exitPayload):
	case <-time.After(runWriteTimeout):
	}
	close(writeCh)
	<-writerDone

	_ = conn.Close(websocket.StatusNormalClosure, "")
	return exitCode
}

// runV2Carriers groups the stdio file descriptors used by a v2 run
// session. Different fields are populated in TTY vs. no-TTY mode.
type runV2Carriers struct {
	// childStdin / childStdout / childStderr are passed to docker.Run
	// as opts.Stdin / opts.Stdout / opts.Stderr. exec.Cmd recognises
	// *os.File and dup2's the fd directly into the child without any
	// helper goroutine.
	childStdin  *os.File
	childStdout *os.File
	childStderr *os.File

	// stdinW is what the WS-read pump writes incoming stdin bytes to.
	// In TTY mode it's the PTY master; in no-TTY mode it's the parent
	// end of the stdin pipe.
	stdinW io.WriteCloser

	// stdoutR / stderrR are what the output pumps read from. In TTY
	// mode stdoutR is the PTY master and stderrR is nil (terminal
	// merges streams). In no-TTY mode each is the read end of a
	// dedicated os.Pipe.
	stdoutR io.ReadCloser
	stderrR io.ReadCloser

	// ptmx is the PTY master fd (TTY mode only) — held separately so
	// we can Setsize it on resize frames.
	ptmx *os.File

	// closers is the full set of fds we opened, in close order.
	closers []io.Closer
	// drainOnce protects closeForPumpDrain.
	drainOnce sync.Once
	// fullOnce protects close.
	fullOnce sync.Once
}

func newRunV2Carriers(tty bool) (*runV2Carriers, error) {
	c := &runV2Carriers{}
	if tty {
		ptmx, pts, err := pty.Open()
		if err != nil {
			return nil, err
		}
		c.ptmx = ptmx
		c.childStdin = pts
		c.childStdout = pts
		c.childStderr = pts
		c.stdinW = ptmx
		c.stdoutR = ptmx
		// pts is closed last so we don't yank the slave out from
		// under the (still spawning) child.
		c.closers = []io.Closer{ptmx, pts}
		return c, nil
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, err
	}
	c.childStdin = stdinR
	c.childStdout = stdoutW
	c.childStderr = stderrW
	c.stdinW = stdinW
	c.stdoutR = stdoutR
	c.stderrR = stderrR
	c.closers = []io.Closer{stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW}
	return c, nil
}

// closeForPumpDrain closes the file descriptors whose closure forces
// the output pumps to observe EOF (or, on PTY, EIO). In no-TTY mode
// that's the parent's writer halves of stdout / stderr; in TTY mode
// it's the PTY master.
func (c *runV2Carriers) closeForPumpDrain() {
	c.drainOnce.Do(func() {
		if c.ptmx != nil {
			_ = c.ptmx.Close()
			return
		}
		// no-TTY: close the parent's stdout/stderr writers (the
		// child still holds its dup2'd copies; this only frees ours)
		// AND close the parent's stdin reader (frees the kernel ref).
		_ = c.childStdout.Close()
		_ = c.childStderr.Close()
		_ = c.childStdin.Close()
	})
}

// close releases everything. Idempotent; safe to call after
// closeForPumpDrain.
func (c *runV2Carriers) close() {
	c.fullOnce.Do(func() {
		for _, cl := range c.closers {
			_ = cl.Close()
		}
	})
}

// pumpV2 reads bytes from r and emits them as channel-prefixed binary
// frames on writeCh. Returns when r is closed.
func pumpV2(wg *sync.WaitGroup, r io.Reader, writeCh chan<- []byte, channel byte) {
	defer wg.Done()
	if r == nil {
		return
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			frame := make([]byte, 1+n)
			frame[0] = channel
			copy(frame[1:], buf[:n])
			writeCh <- frame
		}
		if err != nil {
			return
		}
	}
}

// prependChannel returns a new []byte = channel || payload. Used for
// the exit frame, which is built outside the per-stream pumps.
func prependChannel(channel byte, payload []byte) []byte {
	out := make([]byte, 1+len(payload))
	out[0] = channel
	copy(out[1:], payload)
	return out
}

// sendV2Error fires a single channel-6 frame with msg and closes the
// WS. Used for setup failures before the read/write pumps are running.
func sendV2Error(ctx context.Context, conn *websocket.Conn, msg string) {
	frame := prependChannel(cobaltapi.RunChannelError, []byte(msg))
	wctx, cancel := context.WithTimeout(ctx, runWriteTimeout)
	_ = conn.Write(wctx, websocket.MessageBinary, frame)
	cancel()
	_ = conn.Close(websocket.StatusInternalError, "")
}
