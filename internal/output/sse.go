package output

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/heyblueteam/cobalt/internal/client"
)

// reconnectDelay is how long the resilient follower waits before
// re-opening a dropped deploy-output stream. Short enough that the live
// build view barely stutters, long enough that a genuinely unreachable
// daemon doesn't get hammered (and GetDeployment, which we call each
// iteration, will error out and end the loop anyway).
const reconnectDelay = time.Second

// FollowDeployOutput streams a deployment's output to out and KEEPS
// following until the deployment reaches a terminal state — reconnecting
// from the last byte offset whenever the SSE stream ends early.
//
// A single SSE connection is not a reliable proxy for "the deploy is
// done": a long quiet build step (tsc/vite emit no stdout for minutes)
// can trip a reverse-proxy idle timeout, a transient DB read in the
// daemon's follow loop can end the stream, etc. When that happened we
// used to print "Status: building" and quit mid-build. Now we ask the
// daemon whether the deploy is actually terminal; if not, we re-open the
// stream at the offset we'd reached (the server honors ?offset=N and
// tags every chunk with its byte position as the SSE id) and continue.
//
// Returns nil once the deploy is terminal, or a non-nil error only on a
// genuine failure or context cancellation (Ctrl+C) — preserving the
// contract callers already check with IsContextCanceled.
func FollowDeployOutput(ctx context.Context, cl *client.Client, deploymentID int64, offset int64, out io.Writer) error {
	for {
		lastID, streamErr := streamDeployOutputOnce(ctx, cl, deploymentID, offset, out)
		if streamErr != nil && isContextCanceled(streamErr) {
			return streamErr
		}
		if next, ok := parseOffset(lastID); ok && next > offset {
			offset = next
		}

		// The stream ended. Is the deploy genuinely finished, or did the
		// connection just drop mid-flight? Ask the daemon.
		dep, err := cl.GetDeployment(ctx, deploymentID)
		if err != nil {
			// Can't reach the daemon to decide — surface the stream error
			// if we had one, otherwise the status-check error.
			if streamErr != nil {
				return streamErr
			}
			return fmt.Errorf("check deployment status: %w", err)
		}
		if dep.Status.IsTerminal() {
			return nil
		}

		// Still in flight but the stream dropped — reconnect from offset.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconnectDelay):
		}
	}
}

// streamDeployOutputOnce opens a single SSE connection and pumps it to
// out until the server closes it (or the context is canceled). Returns
// the last SSE event id seen (the byte offset to resume from).
func streamDeployOutputOnce(ctx context.Context, cl *client.Client, deploymentID int64, offset int64, out io.Writer) (string, error) {
	resp, err := cl.DeployOutput(ctx, deploymentID, offset)
	if err != nil {
		return "", fmt.Errorf("connect to deploy output: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		var apiErr struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error != "" {
			return "", fmt.Errorf("%s", apiErr.Error)
		}
		return "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return ConsumeSSE(ctx, resp.Body, out)
}

// parseOffset parses an SSE event id (a byte position) into an offset.
// Returns ok=false for an empty or non-numeric id (e.g. the service-logs
// stream, which emits no ids).
func parseOffset(id string) (int64, bool) {
	if id == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// ConsumeSSE parses a Server-Sent Events stream from r, writing each
// event's data to out. It returns the last event id observed (the deploy-
// output stream sets this to the byte offset, so a resuming caller can
// reconnect with ?offset=N; streams that emit no ids return "").
func ConsumeSSE(ctx context.Context, r io.Reader, out io.Writer) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1024<<10)

	var (
		dataLines []string
		eventID   string
		lastID    string
	)

	flush := func() {
		if len(dataLines) > 0 {
			fmt.Fprintln(out, strings.Join(dataLines, "\n"))
		}
		dataLines = dataLines[:0]
		eventID = ""
	}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return lastID, ctx.Err()
		default:
		}

		line := scanner.Text()
		// Per the SSE spec: a blank line dispatches the buffered event.
		// Multiple `data:` fields within one event are joined by '\n'
		// when delivered to the consumer; previously we overwrote on each
		// `data:` line so multi-line events lost everything but the last.
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "data: "):
			dataLines = append(dataLines, line[6:])
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, line[5:])
		case strings.HasPrefix(line, "id: "):
			eventID = line[4:]
			lastID = eventID
		case strings.HasPrefix(line, "id:"):
			eventID = line[3:]
			lastID = eventID
		}
	}
	// Flush any trailing event the server didn't terminate before EOF.
	flush()

	if err := scanner.Err(); err != nil && !isContextCanceled(err) {
		return lastID, fmt.Errorf("read sse stream: %w", err)
	}
	return lastID, nil
}

func isContextCanceled(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// "deadline exceeded" is what net/http surfaces when the request
	// context is cancelled mid-read — it cannot distinguish a real
	// deadline from a Ctrl+C, so we treat both as quiet exit signals
	// for streaming consumers (logs, deploy follow).
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "Canceled") ||
		strings.Contains(msg, "use of closed network connection")
}
