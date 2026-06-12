package deploy

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

func stopFirstBuilt(name string, stopFirst bool) BuiltService {
	return BuiltService{
		Name: name,
		Service: cobaltfile.Service{
			Type:      cobaltfile.TypeContainer,
			StopFirst: stopFirst,
		},
	}
}

func TestStopFirstPhase_RemovesOnlyOldGenerationsOfFlaggedService(t *testing.T) {
	t.Parallel()
	project := store.Project{ID: 1, Name: "haraka"}
	dep := store.Deployment{Number: 10}
	// Names only: the real listServices populates just Name/Replicas from
	// `docker service ls` — the label-derived ServiceInfo fields stay zero.
	// Constructing fixtures the same way keeps this test honest about what
	// the phase can actually see (the v1.2.0 bug was matching on fields the
	// producer never fills).
	d := &serviceDockerFake{services: []docker.ServiceInfo{
		{Name: "haraka-9-smtp"},
		{Name: "haraka-8-smtp"},
		{Name: "haraka-10-smtp"},    // current gen — keep
		{Name: "haraka-9-acme"},     // not flagged — keep
		{Name: "haraka-9-old-smtp"}, // name collision — keep
	}}
	built := []BuiltService{
		stopFirstBuilt("smtp", true),
		stopFirstBuilt("acme", false),
	}

	if err := stopFirstPhase(context.Background(), d, project, dep, built, io.Discard); err != nil {
		t.Fatalf("stopFirstPhase: %v", err)
	}
	want := map[string]bool{"haraka-9-smtp": true, "haraka-8-smtp": true}
	if len(d.removed) != len(want) {
		t.Fatalf("removed = %v, want exactly %v", d.removed, want)
	}
	for _, name := range d.removed {
		if !want[name] {
			t.Errorf("removed unexpected service %q", name)
		}
	}
}

func TestStopFirstPhase_NoFlaggedServices_TouchesNothing(t *testing.T) {
	t.Parallel()
	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{Number: 2}
	d := &serviceDockerFake{
		services: []docker.ServiceInfo{{Name: "api-1-web"}},
		listErr:  errors.New("must not even list"),
	}
	built := []BuiltService{stopFirstBuilt("web", false)}

	if err := stopFirstPhase(context.Background(), d, project, dep, built, io.Discard); err != nil {
		t.Fatalf("stopFirstPhase: %v", err)
	}
	if len(d.removed) != 0 {
		t.Errorf("removed = %v, want none", d.removed)
	}
}

func TestStopFirstPhase_RemoveFailureIsFatal(t *testing.T) {
	t.Parallel()
	project := store.Project{ID: 1, Name: "haraka"}
	dep := store.Deployment{Number: 10}
	d := &serviceDockerFake{
		services:  []docker.ServiceInfo{{Name: "haraka-9-smtp"}},
		removeErr: errors.New("boom"),
	}
	built := []BuiltService{stopFirstBuilt("smtp", true)}

	if err := stopFirstPhase(context.Background(), d, project, dep, built, io.Discard); err == nil {
		t.Fatal("want error when old service can't be removed (new one would deadlock on the port)")
	}
}
