package docker

import (
	"context"
	"testing"
	"time"
)

func TestWaitForServiceHealthy_AllHealthy(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("service ps api-7-web",
		`{"current_state":"Running 5 minutes ago","health":"healthy"}`+"\n"+
			`{"current_state":"Running 5 minutes ago","health":"healthy"}`+"\n",
	)
	c := NewWithRunner(r)
	if err := c.WaitForServiceHealthy(context.Background(), "api-7-web", 2, 5*time.Second); err != nil {
		t.Errorf("WaitForServiceHealthy: %v", err)
	}
}

func TestWaitForServiceHealthy_FallsBackWhenNoHealthcheck(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	// No health field — service didn't declare a healthcheck. Should
	// accept Running tasks as ready.
	r.answerStdout("service ps api-7-web",
		`{"current_state":"Running 5 minutes ago","health":""}`+"\n"+
			`{"current_state":"Running 5 minutes ago","health":""}`+"\n",
	)
	c := NewWithRunner(r)
	if err := c.WaitForServiceHealthy(context.Background(), "api-7-web", 2, 5*time.Second); err != nil {
		t.Errorf("fallback to task-state failed: %v", err)
	}
}

func TestWaitForServiceHealthy_FailFastOnShutdowns(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("service ps api-7-web",
		`{"current_state":"Shutdown 1 minute ago","health":""}`+"\n"+
			`{"current_state":"Failed 30 seconds ago","health":""}`+"\n"+
			`{"current_state":"Rejected 10 seconds ago","health":""}`+"\n",
	)
	c := NewWithRunner(r)
	err := c.WaitForServiceHealthy(context.Background(), "api-7-web", 1, time.Minute)
	if err == nil {
		t.Error("expected fail-fast error on 3+ shutdowns for 1 replica")
	}
}

func TestWaitForServiceHealthy_TimeoutWhenNotHealthy(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	// Has a healthcheck but only "starting", not yet "healthy".
	r.answerStdout("service ps api-7-web",
		`{"current_state":"Running 1 minute ago","health":"starting"}`+"\n",
	)
	c := NewWithRunner(r)
	err := c.WaitForServiceHealthy(context.Background(), "api-7-web", 1, 100*time.Millisecond)
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestWaitForServiceHealthy_RespectsContextCancellation(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("service ps api-7-web",
		`{"current_state":"Running 1 minute ago","health":"starting"}`+"\n",
	)
	c := NewWithRunner(r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.WaitForServiceHealthy(ctx, "api-7-web", 1, time.Hour)
	if err == nil {
		t.Error("expected ctx error")
	}
}
