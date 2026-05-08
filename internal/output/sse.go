package output

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/heyblueteam/cobalt/internal/client"
)

func FollowDeployOutput(ctx context.Context, cl *client.Client, deploymentID int64, offset int64, out io.Writer) error {
	resp, err := cl.DeployOutput(ctx, deploymentID, offset)
	if err != nil {
		return fmt.Errorf("connect to deploy output: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		var apiErr struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error != "" {
			return fmt.Errorf("%s", apiErr.Error)
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return ConsumeSSE(ctx, resp.Body, out)
}

func ConsumeSSE(ctx context.Context, r io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1024<<10)

	var (
		dataLines []string
		eventID   string
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
			return ctx.Err()
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
		case strings.HasPrefix(line, "id:"):
			eventID = line[3:]
		}
	}
	// Flush any trailing event the server didn't terminate before EOF.
	flush()
	_ = eventID

	if err := scanner.Err(); err != nil && !isContextCanceled(err) {
		return fmt.Errorf("read sse stream: %w", err)
	}
	return nil
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
