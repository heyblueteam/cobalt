package docker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSnapshotStableService(t *testing.T) {
	for _, tc := range []struct {
		name, output     string
		err              error
		wantErr, wantNil bool
	}{
		{name: "full spec", output: `{"Name":"stable","TaskTemplate":{"ContainerSpec":{"Image":"v1"}}}`},
		{name: "missing", err: errNotFound, wantNil: true},
		{name: "daemon failure", err: errors.New("connection refused"), wantErr: true},
		{name: "empty", wantErr: true},
		{name: "null", output: "null", wantErr: true},
		{name: "empty object", output: "{}", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRunner()
			f.answerStdout("version --format", `{"Client":{"ApiVersion":"1.56"},"Server":{"ApiVersion":"1.56","MinAPIVersion":"1.40"}}`)
			f.answerStdout("service inspect", tc.output)
			if tc.err != nil {
				f.answerErr("service inspect", tc.err)
			}
			got, err := NewWithRunner(f).SnapshotStableService(context.Background(), "stable")
			if (err != nil) != tc.wantErr || (got == nil) != (tc.wantNil || tc.wantErr) {
				t.Fatalf("snapshot=%s err=%v", got, err)
			}
		})
	}
}

func TestRestoreStableService(t *testing.T) {
	for _, tc := range []struct {
		name, output    string
		update, wantErr bool
	}{
		{name: "already restored", output: `{"ID":"id","Version":{"Index":1},"Spec":{"Name":"s","Labels":{"b":"2","a":"1"}}}`},
		{name: "restore complete snapshot even after multiple updates", output: `{"ID":"id","Version":{"Index":2},"Spec":{"Name":"new"},"PreviousSpec":{"Name":"intermediate"}}`, update: true},
		{name: "invalid current", output: `null`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRunner()
			f.answerStdout("version --format", `{"Client":{"ApiVersion":"1.56"},"Server":{"ApiVersion":"1.56","MinAPIVersion":"1.40"}}`)
			f.answerStdout("service inspect", tc.output)
			f.answerStdout("system dial-stdio", "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")
			err := NewWithRunner(f).RestoreStableService(context.Background(), "s", json.RawMessage(`{"Name":"s","Labels":{"a":"1","b":"2"}}`))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
			if got := argSequence(f.lastCall().Args, "system", "dial-stdio"); got != tc.update {
				t.Fatalf("updated=%v calls=%v", got, f.calls)
			}
		})
	}
}

func TestRestoreNewStableServiceRetiresBeforeRemoval(t *testing.T) {
	f := newFakeRunner()
	f.answerStdout("service ps", "Shutdown 1 second ago\nFailed 2 seconds ago")
	if err := NewWithRunner(f).RestoreStableService(context.Background(), "s", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 || !argSequence(f.calls[0].Args, "service", "scale", "--detach=true", "s=0") || !argSequence(f.calls[2].Args, "service", "rm", "s") {
		t.Fatalf("calls=%v", f.calls)
	}
}

const recoveryService = `{"Spec":{"TaskTemplate":{"ContainerSpec":{"Labels":{"cobalt.deployment.number":"2"}}},"Mode":{"Replicated":{"Replicas":1}}},"UpdateStatus":{"State":"completed"}}`

func recoveryTask(generation, state, desired string) string {
	return `{"Spec":{"ContainerSpec":{"Labels":{"cobalt.deployment.number":"` + generation + `"}}},"Status":{"State":"` + state + `"},"DesiredState":"` + desired + `"}`
}

func TestStableServiceSettled(t *testing.T) {
	for _, tc := range []struct {
		name, service, ids, tasks string
		settled, wantErr          bool
	}{
		{name: "running replacement", ids: "t1", tasks: "[" + recoveryTask("2", "running", "running") + "]", settled: true},
		{name: "old task still draining", ids: "t1 t2", tasks: "[" + recoveryTask("2", "running", "running") + "," + recoveryTask("1", "running", "shutdown") + "]"},
		{name: "old task terminated", ids: "t1 t2", tasks: "[" + recoveryTask("2", "running", "running") + "," + recoveryTask("1", "shutdown", "shutdown") + "]", settled: true},
		{name: "no tasks"},
		{name: "startup pending", ids: "t1", tasks: "[" + recoveryTask("2", "starting", "running") + "]"},
		{name: "draining current task", ids: "t1", tasks: "[" + recoveryTask("2", "running", "shutdown") + "]"},
		{name: "incomplete inspect", ids: "t1", tasks: "[]", wantErr: true},
		{name: "paused update", service: strings.Replace(recoveryService, "completed", "paused", 1), wantErr: true},
		{name: "update still running", service: strings.Replace(recoveryService, "completed", "updating", 1)},
		{name: "missing generation", service: `{"Spec":{}}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRunner()
			s := tc.service
			if s == "" {
				s = recoveryService
			}
			f.answerStdout("service inspect", s)
			f.answerStdout("service ps", tc.ids)
			f.answerStdout("inspect --type task", tc.tasks)
			settled, err := NewWithRunner(f).stableServiceSettled(context.Background(), "s")
			if settled != tc.settled || (err != nil) != tc.wantErr {
				t.Fatalf("settled=%v err=%v", settled, err)
			}
		})
	}
}

func TestWaitStableServiceSettledHonorsDeadline(t *testing.T) {
	f := newFakeRunner()
	f.answerStdout("service inspect", recoveryService)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := NewWithRunner(f).WaitForStableServiceSettled(ctx, "s"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestRestoreWaitsForControllerAndActualRetirements(t *testing.T) {
	for _, tc := range []struct {
		name, state, tasks string
		blocked            bool
	}{
		{"completed but original still running", "completed", "[" + recoveryTask("1", "running", "shutdown") + "]", true},
		{"paused failed replacement with original still running", "paused", "[" + recoveryTask("1", "running", "shutdown") + "]", true},
		{"controller still updating", "updating", "[]", true},
		{"automatic rollback still active", "rollback_started", "[]", true},
		{"paused failed generation need not be healthy", "paused", "[" + recoveryTask("2", "failed", "shutdown") + "]", false},
		{"completed retired tasks exited", "completed", "[" + recoveryTask("1", "shutdown", "shutdown") + "," + recoveryTask("2", "running", "running") + "]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRunner()
			f.answerStdout("service inspect", `{"ID":"id","Version":{"Index":2},"Spec":{"Name":"new"},"UpdateStatus":{"State":"`+tc.state+`"}}`)
			f.answerStdout("service ps", "task")
			tasks := tc.tasks
			if strings.Contains(tasks, "},{") {
				f.answerStdout("service ps", "task task2")
			}
			f.answerStdout("inspect --type task", tasks)
			f.answerStdout("version --format", `{"Client":{"ApiVersion":"1.56"},"Server":{"ApiVersion":"1.56","MinAPIVersion":"1.40"}}`)
			f.answerStdout("system dial-stdio", "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			err := NewWithRunner(f).RestoreStableService(ctx, "s", json.RawMessage(`{"Name":"old"}`))
			if tc.blocked != errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("blocked=%v err=%v", tc.blocked, err)
			}
			if !tc.blocked && err != nil {
				t.Fatal(err)
			}
			for _, call := range f.calls {
				if tc.blocked && argSequence(call.Args, "system", "dial-stdio") {
					t.Fatal("restoration mutated service before drain")
				}
			}
		})
	}
}
