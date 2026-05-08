package docker

import (
	"context"
	"testing"
	"time"
)

// twoRunningTasks is the canonical "service ps" output for two replicas
// already running. Tests layer in container-side health by also seeding
// docker-ps + docker-inspect responses.
const twoRunningTasks = `{"current_state":"Running 5 minutes ago"}` + "\n" +
	`{"current_state":"Running 5 minutes ago"}` + "\n"

func TestWaitForServiceHealthy_AllHealthy(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("service ps api-7-web", twoRunningTasks)
	r.answerStdout("ps --filter label=com.docker.swarm.service.name=api-7-web", "c1\nc2\n")
	r.answerStdout("inspect --format", "healthy\nhealthy\n")
	c := NewWithRunner(r)
	if err := c.WaitForServiceHealthy(context.Background(), "api-7-web", 2, 5*time.Second); err != nil {
		t.Errorf("WaitForServiceHealthy: %v", err)
	}
}

func TestWaitForServiceHealthy_StartingTimesOut(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("service ps api-7-web", twoRunningTasks)
	r.answerStdout("ps --filter label=com.docker.swarm.service.name=api-7-web", "c1\nc2\n")
	// "starting" is non-empty so the healthcheck path engages, but it's
	// not "healthy" — wait should hit the timeout instead of accepting.
	r.answerStdout("inspect --format", "starting\nstarting\n")
	c := NewWithRunner(r)
	err := c.WaitForServiceHealthy(context.Background(), "api-7-web", 2, 100*time.Millisecond)
	if err == nil {
		t.Error("expected timeout while tasks are starting")
	}
}

func TestWaitForServiceHealthy_FallsBackWhenNoHealthcheck(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("service ps api-7-web", twoRunningTasks)
	r.answerStdout("ps --filter label=com.docker.swarm.service.name=api-7-web", "c1\nc2\n")
	// Both containers report empty health → no HEALTHCHECK declared.
	// Wait should accept Running tasks as ready.
	r.answerStdout("inspect --format", "\n\n")
	c := NewWithRunner(r)
	if err := c.WaitForServiceHealthy(context.Background(), "api-7-web", 2, 5*time.Second); err != nil {
		t.Errorf("fallback to task-state failed: %v", err)
	}
}

func TestWaitForServiceHealthy_FallsBackWhenContainerLookupFails(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	// service ps returns Running tasks, but docker ps errors out.
	// We should still accept Running tasks via the fallback.
	r.answerStdout("service ps api-7-web", twoRunningTasks)
	r.answerErr("ps --filter", staticErr("docker ps boom"))
	c := NewWithRunner(r)
	if err := c.WaitForServiceHealthy(context.Background(), "api-7-web", 2, 5*time.Second); err != nil {
		t.Errorf("fallback after container lookup failure: %v", err)
	}
}

func TestWaitForServiceHealthy_FailFastOnShutdowns(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("service ps api-7-web",
		`{"current_state":"Shutdown 1 minute ago"}`+"\n"+
			`{"current_state":"Failed 30 seconds ago"}`+"\n"+
			`{"current_state":"Rejected 10 seconds ago"}`+"\n",
	)
	r.answerStdout("ps --filter label=com.docker.swarm.service.name=api-7-web", "")
	c := NewWithRunner(r)
	err := c.WaitForServiceHealthy(context.Background(), "api-7-web", 1, time.Minute)
	if err == nil {
		t.Error("expected fail-fast error on 3+ shutdowns for 1 replica")
	}
}

func TestWaitForServiceHealthy_TimeoutWhenNotRunning(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("service ps api-7-web",
		`{"current_state":"Pending 1 second ago"}`+"\n",
	)
	r.answerStdout("ps --filter label=com.docker.swarm.service.name=api-7-web", "")
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
		`{"current_state":"Pending 1 second ago"}`+"\n",
	)
	r.answerStdout("ps --filter label=com.docker.swarm.service.name=api-7-web", "")
	c := NewWithRunner(r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.WaitForServiceHealthy(ctx, "api-7-web", 1, time.Hour)
	if err == nil {
		t.Error("expected ctx error")
	}
}
