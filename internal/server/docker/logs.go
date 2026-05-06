package docker

import (
	"context"
	"io"
)

// ServiceLogs streams a service's logs to w. follow=true keeps the
// stream open and writes new entries as they arrive (the common case
// for `cobalt logs`); follow=false dumps the buffered logs and exits.
//
// Both stdout and stderr are merged into w — there's no useful way for
// callers to distinguish docker's own diagnostic output from app output
// over a single SSE channel anyway.
//
// Note: upstream documents that `docker service logs` occasionally
// hangs for several seconds before producing output (a known docker
// bug). For now we tolerate it; if it becomes a real problem in
// production, add the same 5s-timeout-and-retry workaround upstream
// uses.
func (c *Client) ServiceLogs(ctx context.Context, serviceName string, follow bool, w io.Writer) error {
	args := []string{"service", "logs", "--no-trunc", "--raw"}
	if follow {
		args = append(args, "--follow")
	}
	args = append(args, serviceName)
	return c.runner.Run(ctx, args, nil, w, w)
}
