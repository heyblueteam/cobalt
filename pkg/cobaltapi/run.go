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

// RunSubprotocolV1 is the original JSON-text-frame subprotocol. Kept on
// the server side as a fallback so old CLIs continue to work; new
// clients negotiate v2 (binary channel-prefixed frames, PTY support).
const RunSubprotocolV1 = "cobalt-run.v1"

// RunSubprotocol is an alias for the version current CLIs prefer. Kept
// for backward compatibility with code referencing the old name.
const RunSubprotocol = RunSubprotocolV1

// RunSubprotocolV2 is the kubectl-style binary multiplexed subprotocol.
// Frames are WebSocket binary messages: the first byte is the channel
// ID (see RunChannel* constants below); the remainder is the payload.
// Modeled on Kubernetes's v5.channel.k8s.io: bytes-on-the-wire are
// channel-prefixed, control structures (resize, exit) are JSON-encoded
// payloads on dedicated channels.
const RunSubprotocolV2 = "cobalt-run.v2"

// RunChannel* are the channel-ID bytes used in cobalt-run.v2 binary
// frames. See plans/cobalt/cobalt-run-v2.md for the full protocol spec.
//
// Channel direction summary:
//
//	0 stdin       client → server   raw bytes
//	1 stdout      server → client   raw bytes (also the merged stream in TTY mode)
//	2 stderr      server → client   raw bytes (no-TTY only)
//	3 exit        server → client   JSON {"code":N}; exactly one
//	4 resize      client → server   JSON {"rows":N,"cols":N}; TTY only
//	5 close-stdin client → server   empty payload; server closes its stdin writer
//	6 error       server → client   UTF-8 error string; followed by WS close
const (
	RunChannelStdin      = byte(0)
	RunChannelStdout     = byte(1)
	RunChannelStderr     = byte(2)
	RunChannelExit       = byte(3)
	RunChannelResize     = byte(4)
	RunChannelCloseStdin = byte(5)
	RunChannelError      = byte(6)
)

// RunExitPayload is the JSON shape of a channel-3 frame.
type RunExitPayload struct {
	Code int `json:"code"`
}

// RunResizePayload is the JSON shape of a channel-4 frame.
type RunResizePayload struct {
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}
