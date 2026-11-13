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
	events     []string
	stopErr    string
	ports      map[string][]docker.PublishedPort
	portsErr   error
	releaseErr error
	// restoreFails makes the first n RestoreServicePorts calls fail.
	restoreFails int
}

func (f *publicStopDockerFake) ServicePublishedPorts(_ context.Context, name string) ([]docker.PublishedPort, error) {
	if f.portsErr != nil {
		return nil, f.portsErr
	}
	return f.ports[name], nil
}

func (f *publicStopDockerFake) ReleaseServicePorts(_ context.Context, name string, ports []docker.PublishedPort) error {
	f.events = append(f.events, fmt.Sprintf("release %s %d", name, len(ports)))
	return f.releaseErr
}

func (f *publicStopDockerFake) RestoreServicePorts(_ context.Context, name string, ports []docker.PublishedPort) error {
	f.events = append(f.events, fmt.Sprintf("restore %s %d", name, len(ports)))
	if f.restoreFails > 0 {
		f.restoreFails--
		return errors.New("port still allocated")
	}
	return nil
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

var smtpPorts = []docker.PublishedPort{
	{PublishedAs: 587, FromContainerPort: 587, Protocol: "tcp", Mode: "ingress"},
	{PublishedAs: 465, FromContainerPort: 465, Protocol: "tcp", Mode: "ingress"},
}

// A service scaled to zero keeps its ingress ports, so the replacement could
// not be created (plunk deploy #4). Ports are released only after exit and
// restored before the previous service resumes.
func TestPublicStopFirstReleasesAndRestoresPublishedPorts(t *testing.T) {
	restorePortsBackoff = 0
	ctx := context.Background()
	cf := `{"version":"1.0","services":{"web":{"port":80}}}`
	prev := &store.Deployment{ProjectID: 7, Number: 4, ResolvedCobaltfile: &cf}
	d := &publicStopDockerFake{
		serviceDockerFake: serviceDockerFake{services: []docker.ServiceInfo{{Name: "demo-4-web"}, {Name: "demo-5-web"}}},
		ports:             map[string][]docker.PublishedPort{"demo-4-web": smtpPorts},
		restoreFails:      2,
	}
	s, err := preparePublicStopFirst(ctx, d, &fakeSwapStore{lastSuccess: prev}, store.Project{ID: 7, Name: "demo"}, store.Deployment{Number: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.stopOld(ctx, d, io.Discard); err != nil {
		t.Fatal(err)
	}
	if want := []string{"scale demo-4-web=0", "exited demo-4-web", "release demo-4-web 2"}; !reflect.DeepEqual(d.events, want) {
		t.Fatalf("stop events %v", d.events)
	}
	d.events = nil
	if err := s.recover(ctx, d, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"scale demo-5-web=0", "exited demo-5-web",
		"scale demo-4-web=0", "exited demo-4-web",
		"restore demo-4-web 2", "restore demo-4-web 2", "restore demo-4-web 2",
		"scale demo-4-web=1",
	}
	if !reflect.DeepEqual(d.events, want) {
		t.Fatalf("recover events %v", d.events)
	}
	if !reflect.DeepEqual(d.removed, []string{"demo-5-web"}) {
		t.Fatal(d.removed)
	}
}

func TestPublicStopFirstPortInspectionFailsBeforeMutation(t *testing.T) {
	cf := `{"version":"1.0","services":{"web":{"port":80}}}`
	prev := &store.Deployment{ProjectID: 7, Number: 4, ResolvedCobaltfile: &cf}
	d := &publicStopDockerFake{
		serviceDockerFake: serviceDockerFake{services: []docker.ServiceInfo{{Name: "demo-4-web"}}},
		portsErr:          errors.New("inspect unavailable"),
	}
	_, err := preparePublicStopFirst(context.Background(), d, &fakeSwapStore{lastSuccess: prev}, store.Project{ID: 7, Name: "demo"}, store.Deployment{Number: 5})
	if err == nil || len(d.events) != 0 {
		t.Fatalf("err %v events %v", err, d.events)
	}
}

// A failed release must stop the deploy: the replacement would fail to bind.
// Recovery then restores nothing that was never released.
func TestPublicStopFirstReleaseFailureStopsDeploy(t *testing.T) {
	restorePortsBackoff = 0
	d := &publicStopDockerFake{ports: map[string][]docker.PublishedPort{"demo-4-web": smtpPorts}, releaseErr: errors.New("update rejected")}
	s := &publicStopFirst{
		projectID: 7, newName: "demo-5-web", old: []string{"demo-4-web"},
		previous: &store.Deployment{Number: 4}, previousName: "demo-4-web", previousReplicas: 1,
		ports: d.ports, released: map[string][]docker.PublishedPort{},
	}
	if err := s.stopOld(context.Background(), d, io.Discard); err == nil {
		t.Fatal("release failure ignored")
	}
	d.events = nil
	if err := s.recover(context.Background(), d, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, e := range d.events {
		if e == "restore demo-4-web 2" {
			t.Fatal(d.events)
		}
	}
}

type portPreflightFake struct {
	services []docker.ServiceInfo
	ports    map[string][]docker.PublishedPort
}

func (f *portPreflightFake) ListServicesForProject(context.Context, int64) ([]docker.ServiceInfo, error) {
	return f.services, nil
}

func (f *portPreflightFake) ServicePublishedPorts(_ context.Context, name string) ([]docker.PublishedPort, error) {
	return f.ports[name], nil
}

func TestPublishedPortsPreflight(t *testing.T) {
	project := store.Project{ID: 7, Name: "demo"}
	dep := store.Deployment{Number: 5}
	held := &portPreflightFake{
		services: []docker.ServiceInfo{{Name: "demo-4-web"}, {Name: "demo-5-web"}, {Name: "other-4-web"}},
		ports:    map[string][]docker.PublishedPort{"demo-4-web": smtpPorts, "other-4-web": smtpPorts},
	}
	web := func(stopFirst bool, ports ...int) []BuiltService {
		s := cobaltfile.Service{Type: cobaltfile.TypeContainer, StopFirst: stopFirst}
		for _, p := range ports {
			s.PublishedPorts = append(s.PublishedPorts, cobaltfile.PublishedPort{PublishedAs: p, FromContainerPort: p, Protocol: "tcp"})
		}
		return []BuiltService{{Name: "web", Service: s}}
	}
	for _, tc := range []struct {
		name  string
		d     *portPreflightFake
		built []BuiltService
		fail  bool
	}{
		{"start-first holds same port", held, web(false, 465), true},
		{"stopFirst releases it", held, web(true, 465), false},
		{"different port", held, web(false, 2525), false},
		{"no published ports", held, web(false), false},
		{"first deploy", &portPreflightFake{}, web(false, 587), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := publishedPortsPreflight(context.Background(), tc.d, project, dep, tc.built)
			if (err != nil) != tc.fail {
				t.Fatalf("err %v", err)
			}
		})
	}
}
