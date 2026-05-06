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

type SSEEvent struct {
	ID   string
	Data string
}

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

	var current SSEEvent

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if line == "" {
			if current.Data != "" {
				fmt.Fprintln(out, current.Data)
			}
			current = SSEEvent{}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			current.Data = line[6:]
		} else if strings.HasPrefix(line, "data:") {
			current.Data = line[5:]
		} else if strings.HasPrefix(line, "id: ") {
			current.ID = line[4:]
		} else if strings.HasPrefix(line, "id:") {
			current.ID = line[3:]
		}
	}

	if err := scanner.Err(); err != nil && !isContextCanceled(err) {
		return fmt.Errorf("read sse stream: %w", err)
	}
	return nil
}

func isContextCanceled(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "Canceled") ||
		strings.Contains(msg, "use of closed network connection")
}
