package docker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitForServiceStoppedRequiresExit(t *testing.T) {
	for _, tc := range []struct {
		name, tasks, containers string
		stopped                 bool
	}{
		{"running task", "Running 2 seconds ago", "", false},
		{"pending task", "Preparing 1 second ago", "", false},
		{"orphaned task", "Orphaned 1 second ago", "", false},
		{"local writer outlives task", "Shutdown 1 second ago", "running", false},
		{"paused writer", "Shutdown 1 second ago", "paused", false},
		{"exited", "Shutdown 1 second ago\nFailed 3 seconds ago", "exited", true},
		{"no tasks", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeRunner()
			r.answerStdout("service ps", tc.tasks)
			r.answerStdout("ps --all", tc.containers)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			err := NewWithRunner(r).WaitForServiceStopped(ctx, "demo-1-web")
			if tc.stopped && err != nil {
				t.Fatal(err)
			}
			if !tc.stopped && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("want deadline, got %v", err)
			}
		})
	}
}

func TestWaitForServiceStoppedFailsClosed(t *testing.T) {
	for _, command := range []string{"service ps", "ps --all"} {
		r := newFakeRunner()
		boom := errors.New("inspection unavailable")
		r.answerErr(command, boom)
		if err := NewWithRunner(r).WaitForServiceStopped(context.Background(), "demo-1-web"); !errors.Is(err, boom) {
			t.Fatal(err)
		}
	}
}

func TestServiceReplicasRejectsInvalidInspection(t *testing.T) {
	for _, value := range []string{"", "<nil>", "-1", "global"} {
		r := newFakeRunner()
		r.answerStdout("service inspect", value)
		if _, err := NewWithRunner(r).ServiceReplicas(context.Background(), "demo"); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	r := newFakeRunner()
	r.answerStdout("service inspect", "1\n")
	n, err := NewWithRunner(r).ServiceReplicas(context.Background(), "demo")
	if err != nil || n != 1 {
		t.Fatalf("replicas=%d err=%v", n, err)
	}
	if err := NewWithRunner(r).SetServiceReplicas(context.Background(), "demo", 0); err != nil {
		t.Fatal(err)
	}
	if !argSequence(r.lastCall().Args, "scale", "--detach=true", "demo=0") {
		t.Fatal(r.lastCall())
	}
}
