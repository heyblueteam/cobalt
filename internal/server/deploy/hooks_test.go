package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// fakeHookRunner captures every RunOpts the hook helpers pass to docker
// so tests can assert on what would actually be exec'd, without a daemon.
type fakeHookRunner struct {
	mu    sync.Mutex
	calls []docker.RunOpts
	err   error
}

func (f *fakeHookRunner) Run(_ context.Context, opts docker.RunOpts) error {
	f.mu.Lock()
	f.calls = append(f.calls, opts)
	f.mu.Unlock()
	return f.err
}

func hookCF(hookName string, svc cobaltfile.Service) *cobaltfile.Cobaltfile {
	return &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web":    {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
			hookName: svc,
		},
		Images: map[string]cobaltfile.Image{
			"default": {Dockerfile: "Dockerfile", Context: "."},
		},
	}
}

func TestRunBeforeHook_ThreadsExtraRunParamsAndEnv(t *testing.T) {
	t.Parallel()
	cf := hookCF(cobaltfile.HookDeployStartBefore, cobaltfile.Service{
		Type:           cobaltfile.TypeCommand,
		Image:          "default",
		Port:           cobaltfile.DefaultPort,
		Command:        "npx prisma migrate deploy",
		ExtraRunParams: "--add-host host.docker.internal:host-gateway",
	})
	r := &fakeHookRunner{}
	var out bytes.Buffer

	err := runBeforeHook(
		context.Background(), r,
		store.Project{ID: 7, Name: "api"},
		store.Deployment{Number: 5},
		cf,
		map[string]string{"FOO": "bar"},
		&out, &out,
	)
	if err != nil {
		t.Fatalf("runBeforeHook: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("Run calls: got %d, want 1", len(r.calls))
	}
	opts := r.calls[0]

	if opts.ContainerName != "api-hook-deploy-start-before.5" {
		t.Errorf("ContainerName: got %q", opts.ContainerName)
	}
	if opts.Image != "cobalt/project-api-default:5" {
		t.Errorf("Image: got %q", opts.Image)
	}
	if opts.ProjectID != 7 || opts.ProjectName != "api" {
		t.Errorf("project: id=%d name=%q", opts.ProjectID, opts.ProjectName)
	}
	if opts.ServiceName != cobaltfile.HookDeployStartBefore {
		t.Errorf("ServiceName: got %q", opts.ServiceName)
	}
	if opts.DeploymentNumber != 5 {
		t.Errorf("DeploymentNumber: got %d", opts.DeploymentNumber)
	}
	if got := opts.EnvVars["FOO"]; got != "bar" {
		t.Errorf("EnvVars[FOO]: got %q", got)
	}
	if !slices.Equal(opts.Networks, []string{MainNetworkName}) {
		t.Errorf("Networks: got %v, want [%s]", opts.Networks, MainNetworkName)
	}
	wantExtra := []string{"--add-host", "host.docker.internal:host-gateway"}
	if !slices.Equal(opts.ExtraParams, wantExtra) {
		t.Errorf("ExtraParams: got %v, want %v", opts.ExtraParams, wantExtra)
	}
	wantCmd := []string{"sh", "-c", "npx prisma migrate deploy"}
	if !slices.Equal(opts.Command, wantCmd) {
		t.Errorf("Command: got %v, want %v", opts.Command, wantCmd)
	}
	if !strings.Contains(out.String(), "🪝 running "+cobaltfile.HookDeployStartBefore) {
		t.Errorf("expected start frame, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "✅ "+cobaltfile.HookDeployStartBefore+" complete") {
		t.Errorf("expected complete frame, got: %q", out.String())
	}
}

func TestRunAfterHook_UsesOverrideImage(t *testing.T) {
	t.Parallel()
	cf := &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
			cobaltfile.HookDeployStartAfter: {
				Type:    cobaltfile.TypeCommand,
				Image:   "tools",
				Port:    cobaltfile.DefaultPort,
				Command: "echo done",
			},
		},
		Images: map[string]cobaltfile.Image{
			"default": {Dockerfile: "Dockerfile", Context: "."},
			"tools":   {Dockerfile: "Dockerfile.tools", Context: "."},
		},
	}
	r := &fakeHookRunner{}
	err := runAfterHook(
		context.Background(), r,
		store.Project{ID: 1, Name: "api"},
		store.Deployment{Number: 3},
		cf, nil, io.Discard, io.Discard,
	)
	if err != nil {
		t.Fatalf("runAfterHook: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("calls: %d", len(r.calls))
	}
	if got := r.calls[0].Image; got != "cobalt/project-api-tools:3" {
		t.Errorf("Image: got %q, want cobalt/project-api-tools:3", got)
	}
	if got := r.calls[0].ContainerName; got != "api-hook-deploy-start-after.3" {
		t.Errorf("ContainerName: got %q", got)
	}
}

func TestRunBeforeHook_ThreadsVolumes(t *testing.T) {
	t.Parallel()
	cf := hookCF(cobaltfile.HookDeployStartBefore, cobaltfile.Service{
		Type:    cobaltfile.TypeCommand,
		Image:   "default",
		Port:    cobaltfile.DefaultPort,
		Command: "touch /sentinels/x",
		Volumes: []cobaltfile.Volume{
			{Name: "sentinels", DestinationPath: "/sentinels"},
			{Name: "data", DestinationPath: "/data"},
		},
	})
	r := &fakeHookRunner{}
	err := runBeforeHook(
		context.Background(), r,
		store.Project{ID: 42, Name: "api"},
		store.Deployment{Number: 1},
		cf, nil, io.Discard, io.Discard,
	)
	if err != nil {
		t.Fatalf("runBeforeHook: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("calls: %d", len(r.calls))
	}
	want := []docker.ServiceVolume{
		{VolumeName: "cobalt-volume-42-sentinels", DestinationPath: "/sentinels"},
		{VolumeName: "cobalt-volume-42-data", DestinationPath: "/data"},
	}
	got := r.calls[0].Volumes
	if !slices.Equal(got, want) {
		t.Errorf("Volumes: got %+v, want %+v", got, want)
	}
}

func TestRunHook_NoOpWhenNotDeclared(t *testing.T) {
	t.Parallel()
	cf := &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
		},
		Images: map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	}
	r := &fakeHookRunner{}
	if err := runBeforeHook(context.Background(), r, store.Project{Name: "api"}, store.Deployment{Number: 1}, cf, nil, io.Discard, io.Discard); err != nil {
		t.Errorf("runBeforeHook: %v", err)
	}
	if err := runAfterHook(context.Background(), r, store.Project{Name: "api"}, store.Deployment{Number: 1}, cf, nil, io.Discard, io.Discard); err != nil {
		t.Errorf("runAfterHook: %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("Run should not be called for missing hooks; got %d calls", len(r.calls))
	}
}

func TestRunHook_WrongTypeReturnsError(t *testing.T) {
	t.Parallel()
	cf := hookCF(cobaltfile.HookDeployStartBefore, cobaltfile.Service{
		Type:    cobaltfile.TypeContainer,
		Image:   "default",
		Port:    cobaltfile.DefaultPort,
		Command: "noop",
	})
	r := &fakeHookRunner{}
	err := runBeforeHook(context.Background(), r, store.Project{Name: "api"}, store.Deployment{Number: 1}, cf, nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error for non-command hook")
	}
	if !strings.Contains(err.Error(), "type=command") {
		t.Errorf("error %q does not explain the constraint", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("Run should not be called; got %d", len(r.calls))
	}
}

func TestRunHook_EmptyCommandReturnsError(t *testing.T) {
	t.Parallel()
	cf := hookCF(cobaltfile.HookDeployStartBefore, cobaltfile.Service{
		Type:    cobaltfile.TypeCommand,
		Image:   "default",
		Port:    cobaltfile.DefaultPort,
		Command: "",
	})
	r := &fakeHookRunner{}
	err := runBeforeHook(context.Background(), r, store.Project{Name: "api"}, store.Deployment{Number: 1}, cf, nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
	if !strings.Contains(err.Error(), "no command") {
		t.Errorf("error %q does not mention empty command", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("Run should not be called; got %d", len(r.calls))
	}
}

func TestRunHook_WrapsRunnerError(t *testing.T) {
	t.Parallel()
	underlying := errors.New("docker exit 1")
	cf := hookCF(cobaltfile.HookDeployStartAfter, cobaltfile.Service{
		Type:    cobaltfile.TypeCommand,
		Image:   "default",
		Port:    cobaltfile.DefaultPort,
		Command: "exit 1",
	})
	r := &fakeHookRunner{err: underlying}
	err := runAfterHook(context.Background(), r, store.Project{Name: "api"}, store.Deployment{Number: 1}, cf, nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected wrapped error")
	}
	if !strings.Contains(err.Error(), cobaltfile.HookDeployStartAfter) {
		t.Errorf("error %q should include hook name", err)
	}
	if !errors.Is(err, underlying) {
		t.Errorf("error %v should wrap %v", err, underlying)
	}
}
