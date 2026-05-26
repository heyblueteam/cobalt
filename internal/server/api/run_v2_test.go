package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// dialV2 opens a v2-only WS to the run endpoint.
func (e *runEnv) dialV2(t *testing.T, project, query string) *websocket.Conn {
	t.Helper()
	u, _ := url.Parse(e.srv.URL)
	u.Scheme = "ws"
	u.Path = "/api/projects/" + project + "/run"
	u.RawQuery = query
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		Subprotocols: []string{cobaltapi.RunSubprotocolV2},
	})
	if err != nil {
		t.Fatalf("Dial v2: %v", err)
	}
	if conn.Subprotocol() != cobaltapi.RunSubprotocolV2 {
		t.Fatalf("expected v2 subprotocol, got %q", conn.Subprotocol())
	}
	return conn
}

// v2Frame is one binary frame parsed into channel-id + payload.
type v2Frame struct {
	Channel byte
	Data    []byte
}

// readV2Frames reads binary frames until an exit frame arrives or
// ctx times out.
func readV2Frames(t *testing.T, conn *websocket.Conn, ctx context.Context) []v2Frame {
	t.Helper()
	var frames []v2Frame
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			return frames
		}
		if mt != websocket.MessageBinary || len(data) < 1 {
			continue
		}
		f := v2Frame{Channel: data[0], Data: append([]byte(nil), data[1:]...)}
		frames = append(frames, f)
		if f.Channel == cobaltapi.RunChannelExit {
			return frames
		}
	}
}

func writeV2(t *testing.T, conn *websocket.Conn, ctx context.Context, ch byte, body []byte) {
	t.Helper()
	frame := append([]byte{ch}, body...)
	if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("v2 write: %v", err)
	}
}

func TestRunV2_InjectsProjectAndSyntheticEnv(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	proj, err := e.db.GetProjectByName(context.Background(), "api")
	if err != nil {
		t.Fatalf("GetProjectByName: %v", err)
	}
	if err := e.db.SetEnvVar(context.Background(), proj.ID, "FIREBASE_PRIVATE_KEY", "v2-secret"); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}

	e.docker.onRun = func(_ io.Reader, _, _ io.Writer) error { return nil }

	conn := e.dialV2(t, "api", "command="+url.QueryEscape("true"))
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = readV2Frames(t, conn, ctx)

	got := e.docker.runEnvVars()
	if got["FIREBASE_PRIVATE_KEY"] != "v2-secret" {
		t.Errorf("v2: project env missing: got %q", got["FIREBASE_PRIVATE_KEY"])
	}
	if got["COBALT_PROJECT_NAME"] != "api" {
		t.Errorf("v2: COBALT_PROJECT_NAME: got %q, want %q", got["COBALT_PROJECT_NAME"], "api")
	}
}

func TestRunV2_NegotiatesV2WhenOffered(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	// Container exits immediately so the handler completes.
	e.docker.onRun = func(_ io.Reader, stdout, _ io.Writer) error {
		_, _ = stdout.Write([]byte("hello v2\n"))
		return nil
	}

	conn := e.dialV2(t, "api", "command="+url.QueryEscape("echo hello"))
	defer conn.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	frames := readV2Frames(t, conn, ctx)

	var sawStdout, sawExit bool
	for _, f := range frames {
		switch f.Channel {
		case cobaltapi.RunChannelStdout:
			if strings.Contains(string(f.Data), "hello v2") {
				sawStdout = true
			}
		case cobaltapi.RunChannelExit:
			sawExit = true
			var p cobaltapi.RunExitPayload
			if err := json.Unmarshal(f.Data, &p); err != nil {
				t.Errorf("exit JSON: %v", err)
			}
		}
	}
	if !sawStdout {
		t.Errorf("expected stdout frame containing %q, got %d frames", "hello v2", len(frames))
	}
	if !sawExit {
		t.Error("expected exit frame")
	}
}

func TestRunV2_StderrIsSeparateChannel(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	e.docker.onRun = func(_ io.Reader, stdout, stderr io.Writer) error {
		_, _ = stdout.Write([]byte("OUT\n"))
		_, _ = stderr.Write([]byte("ERR\n"))
		return nil
	}

	conn := e.dialV2(t, "api", "command="+url.QueryEscape("ignored"))
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	frames := readV2Frames(t, conn, ctx)

	var stdoutBytes, stderrBytes []byte
	for _, f := range frames {
		switch f.Channel {
		case cobaltapi.RunChannelStdout:
			stdoutBytes = append(stdoutBytes, f.Data...)
		case cobaltapi.RunChannelStderr:
			stderrBytes = append(stderrBytes, f.Data...)
		}
	}
	if !strings.Contains(string(stdoutBytes), "OUT") {
		t.Errorf("stdout: %q", stdoutBytes)
	}
	if !strings.Contains(string(stderrBytes), "ERR") {
		t.Errorf("stderr: %q", stderrBytes)
	}
}

func TestRunV2_StdinFlowsToContainer(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	// Echo stdin → stdout until stdin closes (cat).
	var got []byte
	var mu sync.Mutex
	done := make(chan struct{})
	e.docker.onRun = func(stdin io.Reader, stdout, _ io.Writer) error {
		defer close(done)
		buf := make([]byte, 1024)
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				mu.Lock()
				got = append(got, buf[:n]...)
				mu.Unlock()
				_, _ = stdout.Write(buf[:n])
			}
			if err != nil {
				return nil
			}
		}
	}

	conn := e.dialV2(t, "api", "command="+url.QueryEscape("cat"))
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	writeV2(t, conn, ctx, cobaltapi.RunChannelStdin, []byte("hi there\n"))
	writeV2(t, conn, ctx, cobaltapi.RunChannelCloseStdin, nil)

	frames := readV2Frames(t, conn, ctx)

	<-done
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(string(got), "hi there") {
		t.Errorf("container stdin: %q", got)
	}
	var sawEcho bool
	for _, f := range frames {
		if f.Channel == cobaltapi.RunChannelStdout && strings.Contains(string(f.Data), "hi there") {
			sawEcho = true
		}
	}
	if !sawEcho {
		t.Errorf("expected stdout to echo stdin, got %d frames", len(frames))
	}
}

func TestRunV2_ResizeFramesAcceptedInTTYMode(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	// Container immediately exits.
	e.docker.onRun = func(_ io.Reader, _, _ io.Writer) error { return nil }

	conn := e.dialV2(t, "api", "command="+url.QueryEscape("true")+"&tty=1")
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	body, _ := json.Marshal(cobaltapi.RunResizePayload{Rows: 24, Cols: 80})
	writeV2(t, conn, ctx, cobaltapi.RunChannelResize, body)

	// We can't observe TIOCSWINSZ from here, but the exit frame
	// confirms the handler didn't choke on the resize frame.
	frames := readV2Frames(t, conn, ctx)
	var sawExit bool
	for _, f := range frames {
		if f.Channel == cobaltapi.RunChannelExit {
			sawExit = true
		}
	}
	if !sawExit {
		t.Error("expected exit frame after resize + container exit")
	}
}

func TestRunV2_NonExitErrorReportsMinusOne(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	e.docker.onRun = func(_ io.Reader, _, _ io.Writer) error { return io.ErrUnexpectedEOF }

	conn := e.dialV2(t, "api", "command="+url.QueryEscape("false"))
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	frames := readV2Frames(t, conn, ctx)

	for _, f := range frames {
		if f.Channel == cobaltapi.RunChannelExit {
			var p cobaltapi.RunExitPayload
			_ = json.Unmarshal(f.Data, &p)
			if p.Code != -1 {
				t.Errorf("exit code: got %d want -1 for non-ExitError", p.Code)
			}
			return
		}
	}
	t.Error("no exit frame")
}

func TestRunV2_RealExitCodePropagated(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	// Simulate the production runner: a wrapped *exec.ExitError.
	e.docker.onRun = func(_ io.Reader, _, _ io.Writer) error {
		realErr := exec.Command("sh", "-c", "exit 42").Run()
		return fmt.Errorf("docker run: %w: simulated", realErr)
	}

	conn := e.dialV2(t, "api", "command="+url.QueryEscape("false"))
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	frames := readV2Frames(t, conn, ctx)

	for _, f := range frames {
		if f.Channel == cobaltapi.RunChannelExit {
			var p cobaltapi.RunExitPayload
			_ = json.Unmarshal(f.Data, &p)
			if p.Code != 42 {
				t.Errorf("exit code: got %d want 42", p.Code)
			}
			return
		}
	}
	t.Error("no exit frame")
}

func TestNewRunLifecycle_CapCancelsContext(t *testing.T) {
	// NOT t.Parallel(): we mutate the package-level cap. Other parallel
	// tests in this package open WebSockets that invoke newRunLifecycle,
	// and a sibling running concurrently would inherit our 100 ms cap and
	// be torn down before its assertions complete.
	original := runMaxLifetime.Load()
	runMaxLifetime.Store(int64(100 * time.Millisecond))
	defer runMaxLifetime.Store(original)

	// We don't actually need a real WebSocket — newRunLifecycle only
	// calls conn.Ping at the heartbeat cadence. A WS that hasn't been
	// pinged yet (because we tear down before the first 30 s tick) is
	// fine.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := newRunLifecycle(r.Context(), conn)
		defer cancel()

		select {
		case <-ctx.Done():
			// good
		case <-time.After(2 * time.Second):
			t.Error("ctx not cancelled within 2s of cap")
		}
	}))
	defer srv.Close()

	u := strings.Replace(srv.URL, "http://", "ws://", 1)
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, u, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseNow()

	// Wait for the server's lifecycle goroutine to fire the cap and
	// return. 1 s is generous given the 100 ms cap.
	time.Sleep(1 * time.Second)
}

func TestRunV2_FallsBackToV1WhenOnlyV1Offered(t *testing.T) {
	t.Parallel()
	e := newRunEnv(t)
	e.seedLiveDeploy("api", `{"version":"1.0","services":{"web":{"port":3000}}}`)

	e.docker.onRun = func(_ io.Reader, stdout, _ io.Writer) error {
		_, _ = stdout.Write([]byte("v1-path"))
		return nil
	}

	u, _ := url.Parse(e.srv.URL)
	u.Scheme = "ws"
	u.Path = "/api/projects/api/run"
	u.RawQuery = "command=" + url.QueryEscape("echo")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		Subprotocols: []string{cobaltapi.RunSubprotocolV1},
	})
	if err != nil {
		t.Fatalf("Dial v1-only: %v", err)
	}
	defer conn.CloseNow()

	if got := conn.Subprotocol(); got != cobaltapi.RunSubprotocolV1 {
		t.Fatalf("subprotocol: got %q want %q", got, cobaltapi.RunSubprotocolV1)
	}

	// v1 path uses JSON text frames; just confirm a stdout frame
	// arrives via the legacy framer.
	mt, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mt != websocket.MessageText {
		t.Errorf("v1 frame type: got %v want text", mt)
	}
	if !strings.Contains(string(data), "v1-path") {
		t.Errorf("v1 frame body: %q", data)
	}
}
