package cobaltapi

// RunFrame is the per-message envelope carried over the cobalt-run WS
// subprotocol. Every message in either direction is JSON-encoded into a
// text frame.
//
// Direction by Type:
//   - stdin  (client → server): bytes to feed the container's stdin
//   - stdout (server → client): bytes from the container's stdout
//   - stderr (server → client): bytes from the container's stderr
//   - exit   (server → client): the container has exited, with Code
//
// Resize frames (TTY support) are deferred to a follow-up — most uses
// of `cobalt run` are non-interactive (database migrations, one-off
// scripts), and TTY proxying through WebSockets adds non-trivial
// complexity.
type RunFrame struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Code int    `json:"code,omitempty"`
}

// Frame types (lowercase per the WS frame.type field).
const (
	RunFrameStdin  = "stdin"
	RunFrameStdout = "stdout"
	RunFrameStderr = "stderr"
	RunFrameExit   = "exit"
)

// RunSubprotocol is the WebSocket subprotocol clients must negotiate to
// connect to GET /api/projects/{name}/run. Bumping the version is how
// future protocol changes are announced.
const RunSubprotocol = "cobalt-run.v1"
