package caddy

import (
	"context"
	"fmt"
	"time"
)

// PatchVerifyError is returned when ServeService PATCH appears to succeed
// but a follow-up GET shows the upstream is still pointed somewhere else.
// This is the symptom upstream's issue #97 produces.
type PatchVerifyError struct {
	ProjectID int64
	Want      string
	Got       string
	Attempts  int
}

func (e *PatchVerifyError) Error() string {
	return fmt.Sprintf(
		"caddy: serve verify drifted after %d attempts: project=%d want upstream=%q got=%q",
		e.Attempts, e.ProjectID, e.Want, e.Got,
	)
}

// defaultVerifyBackoff is the per-attempt sleep schedule between PATCH
// and the final GET. Total wait ≈ 50+100+200+400+800ms = 1.55s. Override
// per-Client via Client.PatchVerifyBackoff.
var defaultVerifyBackoff = []time.Duration{
	50 * time.Millisecond,
	100 * time.Millisecond,
	200 * time.Millisecond,
	400 * time.Millisecond,
	800 * time.Millisecond,
}

// VerifyServeService calls ServeService and then GETs the upstream back to
// confirm it took effect. Caddy's admin API can return 200 from a PATCH
// while internal state silently lags (especially under concurrent reload),
// which is the root of upstream issue #97.
//
// Behavior:
//   - On the first GET attempt that returns the expected upstream, returns nil.
//   - Otherwise, retries with exponential backoff up to len(verifyBackoff)
//     attempts.
//   - On final mismatch, returns *PatchVerifyError.
func (c *Client) VerifyServeService(ctx context.Context, projectID int64, containerName string, port, deploymentNumber int) error {
	if err := c.ServeService(ctx, projectID, containerName, port, deploymentNumber); err != nil {
		return err
	}
	want := containerName

	backoff := c.PatchVerifyBackoff
	if backoff == nil {
		backoff = defaultVerifyBackoff
	}

	var got string
	for attempt, sleep := range backoff {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
		current, err := c.CurrentUpstream(ctx, projectID)
		if err != nil {
			// Transient read failure — keep retrying within the budget.
			got = "<error: " + err.Error() + ">"
			continue
		}
		got = current
		if current == want {
			return nil
		}
		_ = attempt
	}
	return &PatchVerifyError{
		ProjectID: projectID,
		Want:      want,
		Got:       got,
		Attempts:  len(backoff),
	}
}
