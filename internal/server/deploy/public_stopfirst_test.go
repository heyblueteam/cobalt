package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

type publicStopDockerFake struct {
	serviceDockerFake
	events  []string
	stopErr string
}

func (f *publicStopDockerFake) ServiceReplicas(context.Context, string) (int, error) { return 1, nil }
func (f *publicStopDockerFake) SetServiceReplicas(_ context.Context, name string, count int) error {
	f.events = append(f.events, fmt.Sprintf("scale %s=%d", name, count))
	return nil
}

func (f *publicStopDockerFake) WaitForServiceStopped(_ context.Context, name string) error {
	f.events = append(f.events, "exited "+name)
	if name == f.stopErr {
		return errors.New("writer still alive")
	}
	return nil
}

func TestPublicStopFirstRecoveryOrdersWriters(t *testing.T) {
	ctx := context.Background()
	cf := `{"version":"1.0","services":{"web":{"port":3000}}}`
	prev := &store.Deployment{ProjectID: 7, Number: 4, ResolvedCobaltfile: &cf}
	d := &publicStopDockerFake{serviceDockerFake: serviceDockerFake{services: []docker.ServiceInfo{
		{Name: "demo-3-web"}, {Name: "demo-4-web"}, {Name: "demo-5-web"}, {Name: "demo-4-worker"}, {Name: "other-4-web"},
	}}}
	s, err := preparePublicStopFirst(ctx, d, &fakeSwapStore{lastSuccess: prev}, store.Project{ID: 7, Name: "demo"}, store.Deployment{Number: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.stopOld(ctx, d, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := s.recover(ctx, d, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{"scale demo-3-web=0", "exited demo-3-web", "scale demo-4-web=0", "exited demo-4-web", "scale demo-5-web=0", "exited demo-5-web", "scale demo-3-web=0", "exited demo-3-web", "scale demo-4-web=0", "exited demo-4-web", "scale demo-4-web=1"}
	if !reflect.DeepEqual(d.events, want) {
		t.Fatalf("events %v", d.events)
	}
	if !reflect.DeepEqual(d.removed, []string{"demo-5-web"}) {
		t.Fatal(d.removed)
	}
}

func TestPublicStopFirstCannotResumeWhileWriterAlive(t *testing.T) {
	for _, failing := range []string{"demo-5-web", "demo-4-web"} {
		d := &publicStopDockerFake{stopErr: failing, serviceDockerFake: serviceDockerFake{services: []docker.ServiceInfo{{Name: "demo-5-web"}}}}
		s := &publicStopFirst{projectID: 7, newName: "demo-5-web", old: []string{"demo-4-web"}, previous: &store.Deployment{Number: 4}, previousName: "demo-4-web", previousReplicas: 1}
		if err := s.recover(context.Background(), d, io.Discard); err == nil {
			t.Fatal("recovered with live writer")
		}
		for _, event := range d.events {
			if event == "scale demo-4-web=1" {
				t.Fatal(d.events)
			}
		}
	}
}

func TestPublicStopFirstRetainsStableServiceForRecovery(t *testing.T) {
	cf := `{"version":"1.0","stablePublicWeb":true,"services":{"web":{"port":3100}}}`
	prev := &store.Deployment{ProjectID: 7, Number: 4, ResolvedCobaltfile: &cf}
	d := &publicStopDockerFake{serviceDockerFake: serviceDockerFake{services: []docker.ServiceInfo{{Name: "cobalt-web-7"}}}}
	s, err := preparePublicStopFirst(context.Background(), d, &fakeSwapStore{lastSuccess: prev}, store.Project{ID: 7, Name: "demo"}, store.Deployment{Number: 5})
	if err != nil {
		t.Fatal(err)
	}
	if s.previousName != "cobalt-web-7" || s.previousFile.Services["web"].Port != 3100 {
		t.Fatal(s)
	}
	if err := s.stopOld(context.Background(), d, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(d.removed) != 0 {
		t.Fatal("old service spec was destroyed")
	}
	cy := &fakeSwapCaddy{}
	if err := commitCaddySwap(context.Background(), cy, &fakeSwapStore{}, store.Project{ID: 7, Name: "demo"}, *s.previous, s.previousFile); err != nil {
		t.Fatal(err)
	}
	if got := cy.verifyCalls[0]; got.container != "cobalt-web-7" || got.port != 3100 || got.deploymentNumber != 4 {
		t.Fatal(got)
	}
}

func TestPublicStopFirstPreflightFailsBeforeMutation(t *testing.T) {
	missing := `{"version":"1.0","services":{"web":{}}}`
	for _, prev := range []*store.Deployment{
		{Number: 4}, {Number: 4, ResolvedCobaltfile: &missing},
	} {
		d := &publicStopDockerFake{}
		_, err := preparePublicStopFirst(context.Background(), d, &fakeSwapStore{lastSuccess: prev}, store.Project{ID: 7, Name: "demo"}, store.Deployment{Number: 5})
		if err == nil || len(d.events) != 0 {
			t.Fatalf("err %v events %v", err, d.events)
		}
	}
}

func TestPublicStopFirstInitialDeployFailure(t *testing.T) {
	d := &publicStopDockerFake{}
	s, err := preparePublicStopFirst(context.Background(), d, &fakeSwapStore{lastSuccessErr: store.ErrNotFound}, store.Project{ID: 7, Name: "demo"}, store.Deployment{Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	d.services = []docker.ServiceInfo{{Name: "demo-1-web"}}
	if err := s.recover(context.Background(), d, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.removed, []string{"demo-1-web"}) {
		t.Fatal(d.removed)
	}
}

func TestPublicStopFirstDoesNotChangeDefault(t *testing.T) {
	cf, err := cobaltfile.Parse([]byte(`{"version":"1.0","services":{"web":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if publicStopFirstEnabled(cf) || !deferPublicWebCleanup(cf) {
		t.Fatal("default path changed")
	}
	web := cf.Services["web"]
	web.StopFirst = true
	cf.Services["web"] = web
	if !publicStopFirstEnabled(cf) || deferPublicWebCleanup(cf) {
		t.Fatal("stop-first uses grace retirement")
	}
}
